//go:build js && wasm

// Command ezp-wasm exposes the ezp package to JavaScript as globalThis.ezp.
// Every operation returns a Promise; only one operation runs at a time.
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"syscall/js"

	"ezp2023/ezp"
	"ezp2023/ezpmock"
	"ezp2023/webusb"
)

//go:embed chips.dat
var chipsDat []byte

var (
	chips []ezp.Chip

	mu        sync.Mutex
	transport ezp.Transport
	clock     = ezp.Clock12MHz
	readChunk int
	busy      bool
	cancelOp  context.CancelFunc
)

var errBusy = errors.New("another operation is running")

func jsError(err error) js.Value { return js.Global().Get("Error").New(err.Error()) }

// promise runs fn in a goroutine (so it may block on WebUSB) and returns a
// JS Promise settled with its result.
func promise(fn func() (any, error)) js.Value {
	var exec js.Func
	exec = js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			defer exec.Release()
			v, err := fn()
			if err != nil {
				reject.Invoke(jsError(err))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	})
	return js.Global().Get("Promise").New(exec)
}

func bytesToJS(b []byte) js.Value {
	u := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u, b)
	return u
}

func bytesFromJS(v js.Value) []byte {
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}

func chipArg(args []js.Value, i int) (*ezp.Chip, error) {
	if len(args) <= i || args[i].Type() != js.TypeNumber {
		return nil, errors.New("missing chip index")
	}
	n := args[i].Int()
	for k := range chips {
		if chips[k].Index == n {
			return &chips[k], nil
		}
	}
	return nil, fmt.Errorf("unknown chip index %d", n)
}

func progressArg(args []js.Value, i int) ezp.ProgressFunc {
	if len(args) <= i || args[i].Type() != js.TypeFunction {
		return nil
	}
	cb := args[i]
	last := -1
	return func(done, total int) {
		// Throttle to one call per 0.5 %.
		step := 0
		if total > 0 {
			step = done * 200 / total
		}
		if step != last {
			last = step
			cb.Invoke(done, total)
		}
	}
}

// run executes op with a fresh Programmer, refusing concurrent calls.
func run(progress ezp.ProgressFunc, op func(ctx context.Context, p *ezp.Programmer) (any, error)) js.Value {
	return promise(func() (any, error) {
		mu.Lock()
		if busy {
			mu.Unlock()
			return nil, errBusy
		}
		if transport == nil {
			mu.Unlock()
			return nil, errors.New("not connected")
		}
		busy = true
		ctx, cancel := context.WithCancel(context.Background())
		cancelOp = cancel
		p := ezp.New(transport)
		p.Clock, p.ReadChunk, p.Progress = clock, readChunk, progress
		mu.Unlock()

		defer func() {
			mu.Lock()
			busy, cancelOp = false, nil
			mu.Unlock()
			cancel()
		}()
		v, err := op(ctx, p)
		if errors.Is(err, context.Canceled) {
			err = errors.New("cancelled")
		}
		return v, err
	})
}

func deviceInfo(kind, manufacturer, product string, known bool, rev ezp.Revision) js.Value {
	o := js.Global().Get("Object").New()
	o.Set("kind", kind)
	o.Set("manufacturer", manufacturer)
	o.Set("product", product)
	o.Set("knownProduct", known)
	o.Set("revision", rev.String())
	return o
}

func attach(dev js.Value) (any, error) {
	d, err := webusb.Open(dev)
	if err != nil {
		return nil, err
	}
	mu.Lock()
	old := transport
	transport = d
	mu.Unlock()
	if old != nil {
		old.Close()
	}
	return deviceInfo("usb", d.Manufacturer, d.Product, d.KnownProduct, d.Revision()), nil
}

func busName(b byte) string {
	switch b {
	case ezp.BusSPI:
		return "SPI"
	case ezp.BusI2C:
		return "I²C (24xx)"
	case ezp.BusMicrowire:
		return "Microwire (93xx)"
	}
	return "none"
}

func chipsToJS() js.Value {
	arr := js.Global().Get("Array").New(len(chips))
	for i, c := range chips {
		o := js.Global().Get("Object").New()
		o.Set("index", c.Index)
		o.Set("type", c.Type)
		o.Set("vendor", c.Vendor)
		o.Set("model", c.Model)
		o.Set("size", c.Size)
		o.Set("page", c.Page)
		o.Set("protocol", c.Protocol.String())
		o.Set("voltage", c.Voltage.String())
		o.Set("needs18V", c.Needs18VAdapter())
		if c.JEDEC != 0 {
			o.Set("jedec", fmt.Sprintf("%06X", c.JEDEC))
		} else {
			o.Set("jedec", js.Null())
		}
		arr.SetIndex(i, o)
	}
	return arr
}

func main() {
	var err error
	if chips, err = ezp.ParseDat(chipsDat); err != nil {
		panic(err)
	}

	api := js.Global().Get("Object").New()
	api.Set("chips", chipsToJS())
	clocks := js.Global().Get("Array").New()
	for _, n := range ezp.ClockNames {
		clocks.Call("push", n)
	}
	api.Set("clocks", clocks)

	api.Set("supported", js.FuncOf(func(js.Value, []js.Value) any { return webusb.Supported() }))

	// connect() must be called from a click handler: requestDevice is
	// invoked synchronously here to keep the user gesture.
	api.Set("connect", js.FuncOf(func(js.Value, []js.Value) any {
		if !webusb.Supported() {
			return promise(func() (any, error) {
				return nil, errors.New("this browser has no WebUSB; use Chrome, Edge or Opera")
			})
		}
		req := webusb.RequestDevice()
		return promise(func() (any, error) {
			dev, err := webusb.Await(req)
			if err != nil {
				return nil, err
			}
			return attach(dev)
		})
	}))

	// reconnect() reopens a previously allowed device without a picker.
	api.Set("reconnect", js.FuncOf(func(js.Value, []js.Value) any {
		return promise(func() (any, error) {
			if !webusb.Supported() {
				return js.Null(), nil
			}
			list, err := webusb.Await(webusb.PairedDevices())
			if err != nil {
				return nil, err
			}
			for i := 0; i < list.Length(); i++ {
				d := list.Index(i)
				if d.Get("vendorId").Int() == ezp.VendorID && d.Get("productId").Int() == ezp.ProductID {
					return attach(d)
				}
			}
			return js.Null(), nil
		})
	}))

	// connectDemo(chipIndex) attaches a simulated programmer holding that chip.
	api.Set("connectDemo", js.FuncOf(func(_ js.Value, args []js.Value) any {
		c, err := chipArg(args, 0)
		return promise(func() (any, error) {
			if err != nil {
				return nil, err
			}
			d := ezpmock.ForChip(ezp.RevB, c)
			for i := range d.Mem { // something recognisable to read back
				d.Mem[i] = byte(i ^ i>>8)
			}
			mu.Lock()
			old := transport
			transport = d
			mu.Unlock()
			if old != nil {
				old.Close()
			}
			return deviceInfo("demo", "simulated", "Demo with "+c.Vendor+" "+c.Model, true, d.Rev), nil
		})
	}))

	api.Set("disconnect", js.FuncOf(func(js.Value, []js.Value) any {
		return promise(func() (any, error) {
			mu.Lock()
			if busy {
				mu.Unlock()
				return nil, errBusy
			}
			t := transport
			transport = nil
			mu.Unlock()
			if t != nil {
				return nil, t.Close()
			}
			return nil, nil
		})
	}))

	api.Set("cancel", js.FuncOf(func(js.Value, []js.Value) any {
		mu.Lock()
		if cancelOp != nil {
			cancelOp()
		}
		mu.Unlock()
		return nil
	}))

	api.Set("setClock", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			if i := args[0].Int(); i >= 0 && i < len(ezp.ClockNames) {
				mu.Lock()
				clock = ezp.SPIClock(i)
				mu.Unlock()
			}
		}
		return nil
	}))

	// setReadChunk(n): 0 = vendor behaviour (page size), e.g. 65536 for speed.
	api.Set("setReadChunk", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			mu.Lock()
			readChunk = max(args[0].Int(), 0)
			mu.Unlock()
		}
		return nil
	}))

	// detect(chipIndex?) -> {bus, busName, jedec, matches}
	api.Set("detect", js.FuncOf(func(_ js.Value, args []js.Value) any {
		c, _ := chipArg(args, 0) // optional
		return run(nil, func(ctx context.Context, p *ezp.Programmer) (any, error) {
			r, err := p.Detect(ctx, c)
			if err != nil {
				return nil, err
			}
			o := js.Global().Get("Object").New()
			o.Set("bus", int(r.Bus))
			o.Set("busName", busName(r.Bus))
			matches := js.Global().Get("Array").New()
			if r.Bus == ezp.BusSPI {
				o.Set("jedec", fmt.Sprintf("%06X", r.JEDEC))
				for _, m := range ezp.FindByJEDEC(chips, r.JEDEC) {
					matches.Call("push", m.Index)
				}
			} else {
				o.Set("jedec", js.Null())
			}
			o.Set("matches", matches)
			return o, nil
		})
	}))

	api.Set("read", js.FuncOf(func(_ js.Value, args []js.Value) any {
		c, err := chipArg(args, 0)
		return run(progressArg(args, 1), func(ctx context.Context, p *ezp.Programmer) (any, error) {
			if err != nil {
				return nil, err
			}
			b, err := p.Read(ctx, c)
			if err != nil {
				return nil, err
			}
			return bytesToJS(b), nil
		})
	}))

	api.Set("erase", js.FuncOf(func(_ js.Value, args []js.Value) any {
		c, err := chipArg(args, 0)
		return run(progressArg(args, 1), func(ctx context.Context, p *ezp.Programmer) (any, error) {
			if err != nil {
				return nil, err
			}
			return nil, p.Erase(ctx, c)
		})
	}))

	api.Set("write", js.FuncOf(func(_ js.Value, args []js.Value) any {
		c, err := chipArg(args, 0)
		var data []byte
		if err == nil && len(args) > 1 {
			data = bytesFromJS(args[1])
		}
		return run(progressArg(args, 2), func(ctx context.Context, p *ezp.Programmer) (any, error) {
			if err != nil {
				return nil, err
			}
			return nil, p.Write(ctx, c, data)
		})
	}))

	compareResult := func(off int) js.Value {
		o := js.Global().Get("Object").New()
		o.Set("ok", off < 0)
		o.Set("offset", off)
		return o
	}

	api.Set("verify", js.FuncOf(func(_ js.Value, args []js.Value) any {
		c, err := chipArg(args, 0)
		var data []byte
		if err == nil && len(args) > 1 {
			data = bytesFromJS(args[1])
		}
		return run(progressArg(args, 2), func(ctx context.Context, p *ezp.Programmer) (any, error) {
			if err != nil {
				return nil, err
			}
			off, err := p.Verify(ctx, c, data)
			if err != nil {
				return nil, err
			}
			return compareResult(off), nil
		})
	}))

	api.Set("blankCheck", js.FuncOf(func(_ js.Value, args []js.Value) any {
		c, err := chipArg(args, 0)
		return run(progressArg(args, 1), func(ctx context.Context, p *ezp.Programmer) (any, error) {
			if err != nil {
				return nil, err
			}
			off, err := p.BlankCheck(ctx, c)
			if err != nil {
				return nil, err
			}
			return compareResult(off), nil
		})
	}))

	js.Global().Set("ezp", api)
	if f := js.Global().Get("onEzpReady"); f.Type() == js.TypeFunction {
		f.Invoke()
	}
	select {}
}
