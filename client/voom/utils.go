package voom

import (
	"context"
	"fmt"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

func fields(ctx context.Context, c *govmomi.Client) (map[int32]string, error) {
	fm, err := object.GetCustomFieldsManager(c.Client)
	if err != nil {
		return nil, err
	}
	f, err := fm.Field(ctx)
	if err != nil {
		return nil, err
	}

	m := make(map[int32]string)
	for _, x := range f {
		m[x.Key] = x.Name
	}
	return m, nil
}

func get_all_vms(ctx context.Context, c *govmomi.Client) ([]*object.VirtualMachine, error) {
	finder := find.NewFinder(c.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		return nil, err
	}
	finder.SetDatacenter(dc)

	var vms []*object.VirtualMachine
	vms, err = finder.VirtualMachineList(ctx, "*")
	if err != nil {
		return nil, err
	}
	return vms, nil
}

func idle_mem_threshold_info(ctx context.Context, vm *object.VirtualMachine) (bool, int32, float64, float64, error) {
	mvm, err := get_vm_properties(ctx, vm)
	if err != nil {
		return false, -1, -1.0, -1.0, err
	}
	if mvm.Summary.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOff {
		return false, -1, -1.0, -1.0, nil
	}

	idle_mem := mvm.Summary.QuickStats.HostMemoryUsage - mvm.Summary.QuickStats.GuestMemoryUsage
	idle_mem_percent := float64(idle_mem) / float64(mvm.Summary.Config.MemorySizeMB)
	idle_mem_threshold := 0.25
	idle_mem_limit := mvm.Summary.QuickStats.GuestMemoryUsage + int32(float64(mvm.Summary.QuickStats.HostMemoryUsage)*idle_mem_threshold)

	return idle_mem_percent > idle_mem_threshold, idle_mem_limit, idle_mem_percent, idle_mem_threshold, nil
}

func exceeds_idle_threshold(ctx context.Context, vm *object.VirtualMachine) bool {
	exceeds_idle_threshold, _, _, _, err := idle_mem_threshold_info(ctx, vm)
	if err != nil {
		fmt.Printf("ERROR: Failed to get VM properties: %s\n", err.Error())
		return exceeds_idle_threshold
	}

	return exceeds_idle_threshold
}

func has_ballooned_memory(ctx context.Context, vm *object.VirtualMachine) bool {
	mvm, err := get_vm_properties(ctx, vm)
	if err != nil {
		fmt.Printf("ERROR: Failed to get VM properties: %s\n", err.Error())
		return false
	}
	if mvm.Summary.QuickStats.BalloonedMemory != 0 {
		return true
	}

	return false
}

func get_vm_properties(ctx context.Context, vm *object.VirtualMachine) (mo.VirtualMachine, error) {
	var mvm mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), []string{"config", "summary"}, &mvm)
	if err != nil {
		return mo.VirtualMachine{}, err
	}
	return mvm, nil
}

func set_host_property(ctx context.Context, host *object.HostSystem, property string, value interface{}) error {
	m, err := host.ConfigManager().OptionManager(ctx)
	if err != nil {
		return err
	}
	opts, err := m.Query(ctx, property)
	if err != nil {
		return err
	}

	err = m.Update(ctx, []types.BaseOptionValue{&types.OptionValue{
		Key:   property,
		Value: value,
	}})
	if err != nil {
		return err
	}

	fmt.Printf("Updated %s on Host %s from %v to %v\n", property, host.Name(), opts[0].GetOptionValue().Value, value)
	return nil
}

func set_property_on_all_hosts(ctx context.Context, c *govmomi.Client, property string, value interface{}) error {
	finder := find.NewFinder(c.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		return err
	}

	var hosts []*object.HostSystem
	finder.SetDatacenter(dc)
	hosts, err = finder.HostSystemList(ctx, "*")
	if err != nil {
		return err
	}

	for _, host := range hosts {
		err := set_host_property(ctx, host, property, value)
		if err != nil {
			return err
		}
	}

	return nil
}

func set_mem_limit(ctx context.Context, vm *object.VirtualMachine, limit int32) error {
	task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{
		MemoryAllocation: &types.ResourceAllocationInfo{
			Limit: types.NewInt64(int64(limit)),
		},
	})
	if err != nil {
		return err
	}
	err = task.Wait(ctx)
	if err != nil {
		return err
	}
	return nil
}
