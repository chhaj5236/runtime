// Copyright (c) 2017-2018 Intel Corporation
// Copyright (c) 2018 Huawei Corporation
//
// SPDX-License-Identifier: Apache-2.0
//

package drivers

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/kata-containers/runtime/virtcontainers/device/api"
	"github.com/kata-containers/runtime/virtcontainers/device/config"
	"github.com/kata-containers/runtime/virtcontainers/utils"
)

// bind/unbind paths to aid in SRIOV VF bring-up/restore
const (
	pciDriverUnbindPath = "/sys/bus/pci/devices/%s/driver/unbind"
	pciDriverBindPath   = "/sys/bus/pci/drivers/%s/bind"
	vfioNewIDPath       = "/sys/bus/pci/drivers/vfio-pci/new_id"
	vfioRemoveIDPath    = "/sys/bus/pci/drivers/vfio-pci/remove_id"
)

// VFIODevice is a vfio device meant to be passed to the hypervisor
// to be used by the Virtual Machine.
type VFIODevice struct {
	ID         string
	DeviceInfo *config.DeviceInfo
	vfioDevs   []*config.VFIODev
}

// NewVFIODevice create a new VFIO device
func NewVFIODevice(devInfo *config.DeviceInfo) *VFIODevice {
	return &VFIODevice{
		ID:         devInfo.ID,
		DeviceInfo: devInfo,
	}
}

// Attach is standard interface of api.Device, it's used to add device to some
// DeviceReceiver
func (device *VFIODevice) Attach(devReceiver api.DeviceReceiver) error {
	if device.DeviceInfo.Hotplugged {
		return nil
	}

	vfioGroup := filepath.Base(device.DeviceInfo.HostPath)
	iommuDevicesPath := filepath.Join(config.SysIOMMUPath, vfioGroup, "devices")

	deviceFiles, err := ioutil.ReadDir(iommuDevicesPath)
	if err != nil {
		return err
	}

	// Pass all devices in iommu group
	for i, deviceFile := range deviceFiles {
		//Get bdf of device eg 0000:00:1c.0
		deviceBDF, err := getBDF(deviceFile.Name())
		if err != nil {
			return err
		}
		vfio := &config.VFIODev{
			ID:  utils.MakeNameID("vfio", device.DeviceInfo.ID+strconv.Itoa(i), maxDevIDSize),
			BDF: deviceBDF,
		}
		device.vfioDevs = append(device.vfioDevs, vfio)
	}

	// hotplug a VFIO device is actually hotplugging a group of iommu devices
	if err := devReceiver.HotplugAddDevice(device, config.DeviceVFIO); err != nil {
		deviceLogger().WithError(err).Error("Failed to add device")
		return err
	}

	deviceLogger().WithFields(logrus.Fields{
		"device-group": device.DeviceInfo.HostPath,
		"device-type":  "vfio-passthrough",
	}).Info("Device group attached")
	device.DeviceInfo.Hotplugged = true
	return nil
}

// Detach is standard interface of api.Device, it's used to remove device from some
// DeviceReceiver
func (device *VFIODevice) Detach(devReceiver api.DeviceReceiver) error {
	if !device.DeviceInfo.Hotplugged {
		return nil
	}

	// hotplug a VFIO device is actually hotplugging a group of iommu devices
	if err := devReceiver.HotplugRemoveDevice(device, config.DeviceVFIO); err != nil {
		deviceLogger().WithError(err).Error("Failed to remove device")
		return err
	}

	deviceLogger().WithFields(logrus.Fields{
		"device-group": device.DeviceInfo.HostPath,
		"device-type":  "vfio-passthrough",
	}).Info("Device group detached")
	device.DeviceInfo.Hotplugged = false
	return nil
}

// IsAttached checks if the device is attached
func (device *VFIODevice) IsAttached() bool {
	return device.DeviceInfo.Hotplugged
}

// DeviceType is standard interface of api.Device, it returns device type
func (device *VFIODevice) DeviceType() config.DeviceType {
	return config.DeviceVFIO
}

// DeviceID returns device ID
func (device *VFIODevice) DeviceID() string {
	return device.ID
}

// GetDeviceInfo returns device information used for creating
func (device *VFIODevice) GetDeviceInfo() interface{} {
	return device.vfioDevs
}

// getBDF returns the BDF of pci device
// Expected input strng format is [<domain>]:[<bus>][<slot>].[<func>] eg. 0000:02:10.0
func getBDF(deviceSysStr string) (string, error) {
	tokens := strings.Split(deviceSysStr, ":")

	if len(tokens) != 3 {
		return "", fmt.Errorf("Incorrect number of tokens found while parsing bdf for device : %s", deviceSysStr)
	}

	tokens = strings.SplitN(deviceSysStr, ":", 2)
	return tokens[1], nil
}

// BindDevicetoVFIO binds the device to vfio driver after unbinding from host.
// Will be called by a network interface or a generic pcie device.
func BindDevicetoVFIO(bdf, hostDriver, vendorDeviceID string) error {

	// Unbind from the host driver
	unbindDriverPath := fmt.Sprintf(pciDriverUnbindPath, bdf)
	deviceLogger().WithFields(logrus.Fields{
		"device-bdf":  bdf,
		"driver-path": unbindDriverPath,
	}).Info("Unbinding device from driver")

	if err := utils.WriteToFile(unbindDriverPath, []byte(bdf)); err != nil {
		return err
	}

	// Add device id to vfio driver.
	deviceLogger().WithFields(logrus.Fields{
		"vendor-device-id": vendorDeviceID,
		"vfio-new-id-path": vfioNewIDPath,
	}).Info("Writing vendor-device-id to vfio new-id path")

	if err := utils.WriteToFile(vfioNewIDPath, []byte(vendorDeviceID)); err != nil {
		return err
	}

	// Bind to vfio-pci driver.
	bindDriverPath := fmt.Sprintf(pciDriverBindPath, "vfio-pci")

	api.DeviceLogger().WithFields(logrus.Fields{
		"device-bdf":  bdf,
		"driver-path": bindDriverPath,
	}).Info("Binding device to vfio driver")

	// Device may be already bound at this time because of earlier write to new_id, ignore error
	utils.WriteToFile(bindDriverPath, []byte(bdf))

	return nil
}

// BindDevicetoHost binds the device to the host driver driver after unbinding from vfio-pci.
func BindDevicetoHost(bdf, hostDriver, vendorDeviceID string) error {
	// Unbind from vfio-pci driver
	unbindDriverPath := fmt.Sprintf(pciDriverUnbindPath, bdf)
	api.DeviceLogger().WithFields(logrus.Fields{
		"device-bdf":  bdf,
		"driver-path": unbindDriverPath,
	}).Info("Unbinding device from driver")

	if err := utils.WriteToFile(unbindDriverPath, []byte(bdf)); err != nil {
		return err
	}

	// To prevent new VFs from binding to VFIO-PCI, remove_id
	if err := utils.WriteToFile(vfioRemoveIDPath, []byte(vendorDeviceID)); err != nil {
		return err
	}

	// Bind back to host driver
	bindDriverPath := fmt.Sprintf(pciDriverBindPath, hostDriver)
	api.DeviceLogger().WithFields(logrus.Fields{
		"device-bdf":  bdf,
		"driver-path": bindDriverPath,
	}).Info("Binding back device to host driver")

	return utils.WriteToFile(bindDriverPath, []byte(bdf))
}
