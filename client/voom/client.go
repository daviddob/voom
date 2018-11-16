package voom

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

type Client struct {
	url *url.URL
	ctx context.Context
	c   *govmomi.Client
}

func Connect(uri, user, pass string) (*Client, error) {
	u, err := url.Parse(uri + vim25.Path)
	if err != nil {
		return nil, err
	}

	u.User = url.UserPassword(user, pass)

	c := &Client{
		url: u,
		ctx: context.Background(),
	}

	g, err := govmomi.NewClient(c.ctx, c.url, true)
	if err != nil {
		return nil, err
	}
	c.c = g

	return c, nil
}

func (c *Client) Logout() {
	c.c.Logout(c.ctx)
}

func (c *Client) VMs() ([]VM, error) {
	m := view.NewManager(c.c.Client)
	v, err := m.CreateContainerView(c.ctx, c.c.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, err
	}
	defer v.Destroy(c.ctx)

	var vms []mo.VirtualMachine
	err = v.Retrieve(c.ctx, []string{"VirtualMachine"}, []string{}, &vms)
	if err != nil {
		return nil, err
	}

	fields, _ := fields(c.ctx, c.c)
	l := make([]VM, 0)
	for _, vm := range vms {
		if strings.HasPrefix(vm.Summary.Config.Name, "sc-") {
			continue
		}
		v := VM{
			ID:              vm.Summary.Config.Name,
			Uptime:          vm.Summary.QuickStats.UptimeSeconds,
			Type:            vm.Summary.Config.GuestFullName,
			IP:              vm.Summary.Guest.IpAddress,
			On:              vm.Summary.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn,
			MemoryAllocated: vm.Summary.Config.MemorySizeMB,
			MemoryReserved:  vm.Summary.Config.MemoryReservation,
			CPUUsage:        vm.Summary.QuickStats.OverallCpuUsage,
			CPUDemand:       vm.Summary.QuickStats.OverallCpuDemand,
			GuestMemoryUsed: vm.Summary.QuickStats.GuestMemoryUsage,
			HostMemoryUsed:  vm.Summary.QuickStats.HostMemoryUsage,
			CPUs:            vm.Summary.Config.NumCpu,
			DiskAllocated:   vm.Summary.Storage.Committed + vm.Summary.Storage.Uncommitted,
			DiskUsed:        vm.Summary.Storage.Committed,
			DiskFree:        vm.Summary.Storage.Uncommitted,

			Tags: make(map[string]string),
		}

		for _, cv := range vm.CustomValue {
			v.Tags[fields[cv.GetCustomFieldValue().Key]] = cv.(*types.CustomFieldStringValue).Value
		}

		l = append(l, v)
	}

	return l, nil
}

func (c *Client) ReclaimMemory() error {
	finder := find.NewFinder(c.c.Client, true)
	dc, err := finder.DefaultDatacenter(c.ctx)
	if err != nil {
		return err
	}
	finder.SetDatacenter(dc)

	fmt.Printf("Finding vms in DC:%s\n", dc.Name())
	var vms []*object.VirtualMachine
	vms, err = finder.VirtualMachineList(c.ctx, "*")
	if err != nil {
		fmt.Println("Foo")
		return err
	}

	for _, vm := range vms {
		if strings.HasPrefix(vm.Name(), "sc-") {
			continue
		}
		var mvm mo.VirtualMachine
		err := vm.Properties(c.ctx, vm.Reference(), []string{"config", "summary"}, &mvm)
		if err != nil {
			return err
		}

		fmt.Printf("VM:%s Allocated:  Used: PercentIdle: Limit: %d\n", mvm.Summary.Config.Name, *mvm.Config.MemoryAllocation.Limit)
	}
	return nil
}
