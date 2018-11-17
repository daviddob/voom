package voom

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/vmware/govmomi"
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
			IdleMemory:      vm.Summary.QuickStats.HostMemoryUsage - vm.Summary.QuickStats.GuestMemoryUsage,
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
	vms, err := get_all_vms(c.ctx, c.c)
	if err != nil {
		return err
	}

	// mem.ctlmaxpercent 65 -> 95 (allow balloon driver to absorb most idle ram)
	err = set_property_on_all_hosts(c.ctx, c.c, "Mem.CtlMaxPercent", 65)
	if err != nil {
		return err
	}

	for _, vm := range vms {
		if strings.HasPrefix(vm.Name(), "sc-") {
			continue
		}

		// fmt.Printf("VM:%s Allocated: %d HostUsed: %d GuestUsed: %d GuestIdleMem: %d Balooned: %d PercentBalooned: %f HostIdlePercent: %f PercentIdle: %f Limit: %d ToolsVersion: %d\n",
		// 	mvm.Summary.Config.Name,
		// 	mvm.Summary.Config.MemorySizeMB,
		// 	mvm.Summary.QuickStats.HostMemoryUsage,
		// 	mvm.Summary.QuickStats.GuestMemoryUsage,
		// 	idle_mem,
		// 	mvm.Summary.QuickStats.BalloonedMemory,
		// 	float64(mvm.Summary.QuickStats.BalloonedMemory)/float64(mvm.Summary.Config.MemorySizeMB),
		// 	idle_mem_percent,
		// 	float64(mvm.Summary.QuickStats.GuestMemoryUsage)/float64(mvm.Summary.QuickStats.HostMemoryUsage),
		// 	*mvm.Config.MemoryAllocation.Limit,
		// 	mvm.Config.Tools.ToolsVersion)

		// fmt.Printf("VM:%s Allocated: %d GuestMem: %d GuestIdleMem: %d PercentBalooned: %f HostIdlePercent: %f Cleanup?: %t LimitThreshold: %d\n",
		// 	mvm.Summary.Config.Name,
		// 	mvm.Summary.Config.MemorySizeMB,
		// 	mvm.Summary.QuickStats.GuestMemoryUsage,
		// 	idle_mem,
		// 	float64(mvm.Summary.QuickStats.BalloonedMemory)/float64(mvm.Summary.Config.MemorySizeMB),
		// 	idle_mem_percent,
		// 	idle_mem_percent > idle_mem_threshold,
		// 	idle_mem_limit)

		exceeds_threshold, idle_mem_limit, idle_mem_percent, idle_mem_threshold, err := idle_mem_threshold_info(c.ctx, vm)
		if err != nil {
			return err
		}

		if exceeds_threshold {
			fmt.Printf("Setting temporary VM Limit: %d for VM: %s\n", idle_mem_limit, vm.Name())
			//Set temporary mem limit
			err = set_mem_limit(c.ctx, vm, idle_mem_limit)
			if err != nil {
				return err
			}

			rounds_since_update := 0
			previous_idle_mem_percent := -1.0
			for exceeds_threshold {
				exceeds_threshold, idle_mem_limit, idle_mem_percent, idle_mem_threshold, err = idle_mem_threshold_info(c.ctx, vm)
				if err != nil {
					//Remove limit
					return err
				}
				fmt.Printf("Waiting for IdleMemPercent: %f to drop below Threshold: %f for VM: %s\n", idle_mem_percent, idle_mem_threshold, vm.Name())
				if previous_idle_mem_percent-0.05 <= idle_mem_percent {
					rounds_since_update++
				} else {
					rounds_since_update = 0
				}
				previous_idle_mem_percent = idle_mem_percent
				if rounds_since_update == 10 {
					fmt.Printf("VM %s failed to respond to ballooning limit\n", vm.Name())
					break
				}
				time.Sleep(30 * time.Second)
			}

			fmt.Printf("Removing temporary VM Limit: %d for VM: %s\n", idle_mem_limit, vm.Name())
			//Remove temporary mem limit
			err = set_mem_limit(c.ctx, vm, -1)
			if err != nil {
				return err
			}
		}
	}

	// mem.ctlmaxpercent 95 -> 0 (disable balloon driver to "free" guest mem
	// and allow for expansion as needed)
	fmt.Printf("Draining Ballooned Memory\n")
	err = set_property_on_all_hosts(c.ctx, c.c, "Mem.CtlMaxPercent", 0)
	if err != nil {
		return err
	}

	balloon_drained := false
	for !balloon_drained {
		fmt.Printf("Checking VMs for Ballooned Memory\n")
		balloon_drained = true
		for _, vm := range vms {
			if has_ballooned_memory(c.ctx, vm) {
				fmt.Printf("VM %s still has Ballooned memory\n", vm.Name())
				balloon_drained = false
			}
		}
		time.Sleep(30 * time.Second)
	}
	// wait for MCTLSZ to be 0 on all vms (collapse balloon driver)

	// mem.ctlmaxpercent 95 -> 65 (reset host defaults)
	err = set_property_on_all_hosts(c.ctx, c.c, "Mem.CtlMaxPercent", 65)
	if err != nil {
		return err
	}

	return nil
}
