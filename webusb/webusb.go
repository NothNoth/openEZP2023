//go:build js && wasm

// Package webusb implements ezp.Transport on top of the browser WebUSB API
// (Chrome, Edge, Opera; not Firefox or Safari).
//
// All blocking methods wait on JavaScript promises, so they must be called
// from a goroutine, never directly from a js.FuncOf callback.
package webusb

import (
	"errors"
	"fmt"
	"syscall/js"

	"ezp2023/ezp"
)

// Await blocks the calling goroutine until the promise settles.
func Await(promise js.Value) (js.Value, error) {
	type result struct {
		v   js.Value
		err error
	}
	ch := make(chan result, 1)
	var onOK, onErr js.Func
	onOK = js.FuncOf(func(_ js.Value, args []js.Value) any {
		v := js.Undefined()
		if len(args) > 0 {
			v = args[0]
		}
		ch <- result{v: v}
		return nil
	})
	onErr = js.FuncOf(func(_ js.Value, args []js.Value) any {
		msg := "promise rejected"
		if len(args) > 0 {
			if m := args[0].Get("message"); m.Type() == js.TypeString {
				msg = m.String()
			} else {
				msg = args[0].Call("toString").String()
			}
		}
		ch <- result{err: errors.New(msg)}
		return nil
	})
	promise.Call("then", onOK, onErr)
	r := <-ch
	onOK.Release()
	onErr.Release()
	return r.v, r.err
}

// Supported reports whether the browser exposes navigator.usb.
func Supported() bool {
	return js.Global().Get("navigator").Get("usb").Truthy()
}

// RequestDevice opens the browser device picker filtered on the EZP2023+.
// It must be called synchronously from a user gesture (a click handler);
// pass the returned promise to Open from a goroutine.
func RequestDevice() js.Value {
	filter := js.Global().Get("Object").New()
	filter.Set("vendorId", ezp.VendorID)
	filter.Set("productId", ezp.ProductID)
	opts := js.Global().Get("Object").New()
	opts.Set("filters", js.Global().Get("Array").New(filter))
	return js.Global().Get("navigator").Get("usb").Call("requestDevice", opts)
}

// PairedDevices returns a promise for devices the user already allowed
// (navigator.usb.getDevices), which reconnect without a picker.
func PairedDevices() js.Value {
	return js.Global().Get("navigator").Get("usb").Call("getDevices")
}

// Device is an opened EZP2023+.
type Device struct {
	dev          js.Value
	rev          ezp.Revision
	ep           ezp.Endpoints
	iface        int
	Manufacturer string
	Product      string
	// KnownProduct is false when the product string matched neither
	// revision and RevA was assumed.
	KnownProduct bool
}

// Open opens and claims a USBDevice (the resolved value of RequestDevice
// or one entry of PairedDevices).
func Open(dev js.Value) (*Device, error) {
	if !dev.Get("opened").Bool() {
		if _, err := Await(dev.Call("open")); err != nil {
			return nil, fmt.Errorf("webusb: open: %w (on Windows the device must use the WinUSB driver; on Linux check the udev rule)", err)
		}
	}
	if dev.Get("configuration").IsNull() {
		if _, err := Await(dev.Call("selectConfiguration", 1)); err != nil {
			return nil, fmt.Errorf("webusb: select configuration: %w", err)
		}
	}
	ifaces := dev.Get("configuration").Get("interfaces")
	if ifaces.Length() == 0 {
		return nil, errors.New("webusb: device has no interface")
	}
	num := ifaces.Index(0).Get("interfaceNumber").Int()
	if _, err := Await(dev.Call("claimInterface", num)); err != nil {
		return nil, fmt.Errorf("webusb: claim interface %d: %w", num, err)
	}
	d := &Device{dev: dev, iface: num}
	if v := dev.Get("manufacturerName"); v.Type() == js.TypeString {
		d.Manufacturer = v.String()
	}
	if v := dev.Get("productName"); v.Type() == js.TypeString {
		d.Product = v.String()
	}
	d.rev, d.KnownProduct = ezp.RevisionFromProduct(d.Product)
	d.ep = d.rev.Endpoints()
	return d, nil
}

func (d *Device) Revision() ezp.Revision { return d.rev }

func (d *Device) out(ep int, p []byte) error {
	buf := js.Global().Get("Uint8Array").New(len(p))
	js.CopyBytesToJS(buf, p)
	res, err := Await(d.dev.Call("transferOut", ep, buf))
	if err != nil {
		return err
	}
	if s := res.Get("status").String(); s != "ok" {
		if s == "stall" {
			Await(d.dev.Call("clearHalt", "out", ep))
		}
		return fmt.Errorf("webusb: transferOut ep %d: %s", ep, s)
	}
	if n := res.Get("bytesWritten").Int(); n != len(p) {
		return fmt.Errorf("webusb: short write on ep %d: %d/%d", ep, n, len(p))
	}
	return nil
}

func (d *Device) WriteCommand(p []byte) error { return d.out(d.ep.Cmd, p) }
func (d *Device) WriteData(p []byte) error    { return d.out(d.ep.DataOut, p) }

func (d *Device) Read(n int) ([]byte, error) {
	res, err := Await(d.dev.Call("transferIn", d.ep.In, n))
	if err != nil {
		return nil, err
	}
	if s := res.Get("status").String(); s != "ok" {
		if s == "stall" {
			Await(d.dev.Call("clearHalt", "in", d.ep.In))
		}
		return nil, fmt.Errorf("webusb: transferIn ep %d: %s", d.ep.In, s)
	}
	dv := res.Get("data")
	u8 := js.Global().Get("Uint8Array").New(dv.Get("buffer"), dv.Get("byteOffset"), dv.Get("byteLength"))
	out := make([]byte, u8.Length())
	js.CopyBytesToGo(out, u8)
	return out, nil
}

func (d *Device) Close() error {
	Await(d.dev.Call("releaseInterface", d.iface))
	_, err := Await(d.dev.Call("close"))
	return err
}
