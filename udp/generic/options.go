package generic

import "net"

func DefaultMulticastOptions() *MulticastOptions {
	return &MulticastOptions{
		HopLimit: 1,
	}
}

type MulticastOptions struct {
	Iface    *net.Interface
	Source   *net.IP
	HopLimit int
}

func (m *MulticastOptions) Apply(o MulticastOption) {
	o.applyMC(m)
}

// A MulticastOption sets options such as hop limit, etc.
type MulticastOption interface {
	applyMC(*MulticastOptions)
}

type MulticastInterfaceOpt struct {
	iface net.Interface
}

func (m MulticastInterfaceOpt) applyMC(o *MulticastOptions) {
	o.Iface = &m.iface
}

func WithMulticastInterface(iface net.Interface) MulticastOption {
	return &MulticastInterfaceOpt{iface: iface}
}

type MulticastHoplimitOpt struct {
	hoplimit int
}

func (m MulticastHoplimitOpt) applyMC(o *MulticastOptions) {
	o.HopLimit = m.hoplimit
}
func WithMulticastHoplimit(hoplimit int) MulticastOption {
	return &MulticastHoplimitOpt{hoplimit: hoplimit}
}

type MulticastSourceOpt struct {
	source net.IP
}

func (m MulticastSourceOpt) applyMC(o *MulticastOptions) {
	o.Source = &m.source
}
func WithMulticastSource(source net.IP) MulticastOption {
	return &MulticastSourceOpt{source: source}
}
