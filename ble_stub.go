//go:build !windows

package main

import "errors"

var errUnsupported = errors.New("蓝牙功能仅支持 Windows（依赖 bluetoothapis.dll）")

// ListDevices 在非 Windows 平台不可用。
func ListDevices() ([]DeviceInfo, error) { return nil, errUnsupported }

// BLEConn 占位类型，保证跨平台可编译。
type BLEConn struct{}

// Open 在非 Windows 平台不可用。
func Open(path, preferWrite, preferNotify string, noResp bool) (*BLEConn, error) {
	return nil, errUnsupported
}

func (c *BLEConn) Write([]byte) error           { return errUnsupported }
func (c *BLEConn) Notifications() <-chan []byte { return nil }
func (c *BLEConn) Close() error                 { return nil }
func (c *BLEConn) Describe() LinkInfo           { return LinkInfo{} }
func (c *BLEConn) WriteUUID() string            { return "" }
func (c *BLEConn) NotifyUUID() string           { return "" }
