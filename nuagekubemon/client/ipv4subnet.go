/*
###########################################################################
#
#   Filename:           ipv4subnet.go
#
#   Author:             Ryan Fredette
#   Created:            August 24, 2015
#
#   Description:        IPv4 Address, Subnet, and Subnet Pool management
#
###########################################################################
#
#              Copyright (c) 2015 Nuage Networks
#
###########################################################################

*/

package client

import (
	"errors"
	"fmt"
)

type IPv4Address [4]uint8

func (addr IPv4Address) String() string {
	return fmt.Sprintf("%v.%v.%v.%v", addr[0], addr[1], addr[2], addr[3])
}

type IPv4Subnet struct {
	Address  IPv4Address
	CIDRMask int //e.g. 24, not 255.255.255.0
}

func IPv4SubnetFromString(input string) (*IPv4Subnet, error) {
	output := &IPv4Subnet{}
	n, err := fmt.Sscanf(input, "%d.%d.%d.%d/%d", &output.Address[0],
		&output.Address[1], &output.Address[2], &output.Address[3],
		&output.CIDRMask)
	if err != nil {
		return nil, err
	}
	if n != 5 {
		return nil, errors.New(fmt.Sprintf(
			"Invalid syntax in input string %q", input))
	}
	return output, nil
}

func (subnet IPv4Subnet) String() string {
	return fmt.Sprintf("%v/%v", subnet.Address, subnet.CIDRMask)
}

func (subnet IPv4Subnet) Netmask() IPv4Address {
	// returns the traditional IPv4 netmask instead of the CIDR
	// e.g. .../24 would return 255.255.255.0
	if subnet.CIDRMask >= 32 {
		return IPv4Address{255, 255, 255, 255}
	}
	fullmask := uint((1 << 32) - (1 << uint(32-subnet.CIDRMask)))
	return IPv4Address{
		uint8((fullmask / uint(1<<24)) % 256),
		uint8((fullmask / uint(1<<16)) % 256),
		uint8((fullmask / 256) % 256),
		uint8(fullmask % 256),
	}
}

func (subnet *IPv4Subnet) Split() (*IPv4Subnet, *IPv4Subnet, error) {
	if subnet.CIDRMask >= 32 {
		return nil, nil, errors.New("Cannot split /32 address space")
	}
	loSubnet, hiSubnet := &IPv4Subnet{}, &IPv4Subnet{}
	for i, mask := 0, subnet.CIDRMask; i < 4; i++ {
		switch {
		case mask >= 8:
			loSubnet.Address[i] = subnet.Address[i]
			hiSubnet.Address[i] = subnet.Address[i]
			mask -= 8
		case mask > 0:
			bitmask := uint8(uint(256-(1<<uint(8-mask))) % 256)
			loSubnet.Address[i] = subnet.Address[i] & bitmask
			hiSubnet.Address[i] = subnet.Address[i] & bitmask
			mask = 0
		}
	}
	loSubnet.CIDRMask = subnet.CIDRMask + 1
	hiSubnet.CIDRMask = subnet.CIDRMask + 1
	index := subnet.CIDRMask / 8
	offset := uint(subnet.CIDRMask % 8)
	bit := uint8(128) >> offset
	loSubnet.Address[index] &= ^bit
	hiSubnet.Address[index] |= bit
	return loSubnet, hiSubnet, nil
}

// Compare `a` to `b`.  If `a > b`, the result will be positive.  If `a < b`,
// the result will be negative.  If `a == b`, the result will be 0.
func (a *IPv4Subnet) Compare(b *IPv4Subnet) int {
	// For sorting purposes, a subnet with a smaller mask (larger size) will
	// always be greater than a subnet with a larger mask.
	if n := b.CIDRMask - a.CIDRMask; n != 0 {
		return n
	}
	index := a.CIDRMask / 8
	mask := uint8((256 - uint(1<<uint(8-(a.CIDRMask%8)))) % 256)
	return int((a.Address[index] & mask) - (b.Address[index] & mask))
}

func CanMerge(a, b *IPv4Subnet) bool {
	// We can't merge the /0 address space.
	if a.CIDRMask <= 0 || b.CIDRMask <= 0 {
		return false
	}
	// An address can't be merged with itself.
	if a.Compare(b) == 0 {
		return false
	}
	// Addresses with different netmasks can't be merged.
	if a.CIDRMask != b.CIDRMask {
		return false
	}
	aCopy := &IPv4Subnet{a.Address, a.CIDRMask - 1}
	bCopy := &IPv4Subnet{b.Address, b.CIDRMask - 1}
	return aCopy.Compare(bCopy) == 0
}

func Merge(a, b *IPv4Subnet) (*IPv4Subnet, error) {
	if !CanMerge(a, b) {
		return nil, errors.New(fmt.Sprintf("Can't merge subnets %s and %s!", a, b))
	}
	newSubnet := &IPv4Subnet{a.Address, a.CIDRMask - 1}
	index := newSubnet.CIDRMask / 8
	mask := uint8(uint(1<<8 - 1<<uint(8-(newSubnet.CIDRMask%8))))
	newSubnet.Address[index] &= mask
	return newSubnet, nil
}

type IPv4SubnetNode struct {
	subnet *IPv4Subnet
	next   *IPv4SubnetNode
}

type IPv4SubnetPool [33]*IPv4SubnetNode

/* A subnet pool is an array of linked lists.  Each list consists only of
 * subnets with the same CIDR netmask (/0 - /32).  When allocating a subnet
 * with netmask X, the pool will first attempt to pick a subnet of the exact
 * size.  If one is not available, it will get a subnet with netmask X-1, then
 * split it to create 2 subnets with netmask X.  It will return 1 of those
 * subnets to the pool, then return the other one.
 */
func (pool *IPv4SubnetPool) Alloc(size int) (*IPv4Subnet, error) {
	if size < 0 || size > 32 {
		return nil, errors.New("Invalid subnet size.  Expected between /0 and /32")
	}
	// If there's already at least 1 subnet of the intended size, remove it
	// from the list and return it.
	if pool[size] != nil {
		node := pool[size]
		pool[size] = node.next
		return node.subnet, nil
	}
	// If not, get a larger subnet (1 CIDR mask less), and split it to create 2
	// subnets of the expected size.
	bigSubnet, err := pool.Alloc(size - 1)
	if err != nil {
		return nil, err
	}
	loSubnet, hiSubnet, err := bigSubnet.Split()
	if err != nil {
		pool.Free(bigSubnet)
		return nil, err
	}
	// Of the two subnets from the split, only one is needed, so release the other.
	err = pool.Free(hiSubnet)
	if err != nil {
		pool.Free(bigSubnet)
		return nil, err
	}
	return loSubnet, nil
}

/* When freeing a subnet, first the pool should be checked for another subnet
 * with the same netmask that it can be merged with (e.g. 10.0.0.0/25 and
 * 10.0.0.128/25 can be merged into 10.0.0.0/24).  If a merge can be done, both
 * subnets should temporarily be allocated, the subnets merged, then the merged
 * subnet should be freed.
 *
 * I've had some issues with figuring out a fast way to check if they can be
 * merged, so for the current version, no merge checks are made.  In the
 * current implementation, we will always request a /24 subnet, so eventually
 * the entire pool will gravitate toward fragmenting at the /24 level.  Because
 * that's the size we care about, it shouldn't be an issue until the
 * implementation requires bigger subnets to be available.
 */
func (pool *IPv4SubnetPool) Free(subnet *IPv4Subnet) error {
	if subnet.CIDRMask < 0 || subnet.CIDRMask > 32 {
		return errors.New(fmt.Sprintf("Cannot free bad subnet %s", subnet))
	}
	var prev, curr *IPv4SubnetNode
	curr = pool[subnet.CIDRMask]
	// If there's nothing in the list, or the current subnet would sort before
	// the one at the beginning of this list, insert it first.
	if curr == nil || subnet.Compare(curr.subnet) < 0 {
		pool[subnet.CIDRMask] = &IPv4SubnetNode{subnet, curr}
		return nil
	}
	prev = curr
	curr = curr.next
	for curr != nil {
		switch {
		case subnet.Compare(curr.subnet) == 0:
			return errors.New(fmt.Sprintf("Double free of %s", subnet))
		case subnet.Compare(curr.subnet) < 0:
			prev.next = &IPv4SubnetNode{subnet, curr}
			return nil
		}
		prev = curr
		curr = curr.next
	}
	// We reached the end of the list (prev.next is nil), so add the subnet to
	// the end of it.
	prev.next = &IPv4SubnetNode{subnet, nil}
	return nil
}
