package generic

import "net"

func DefaultMulticastOptions() MulticastOptions {
	return MulticastOptions{
		IFaceMode: MulticastAllInterface,
		HopLimit:  1,
	}
}

type MulticastInterfaceMode int

const (
	MulticastAllInterface      MulticastInterfaceMode = 0
	MulticastAnyInterface      MulticastInterfaceMode = 1
	MulticastSpecificInterface MulticastInterfaceMode = 2
)

type MulticastOptions struct {
	IFaceMode MulticastInterfaceMode
	Iface     net.Interface
	Source    *net.IP
	HopLimit  int
}

func (m *MulticastOptions) Apply(o MulticastOption) {
	o.applyMC(m)
}

// A MulticastOption sets options such as hop limit, etc.
type MulticastOption interface {
	applyMC(*MulticastOptions)
}

type MulticastInterfaceModeOpt struct {
	mode MulticastInterfaceMode
}

func (m MulticastInterfaceModeOpt) applyMC(o *MulticastOptions) {
	o.IFaceMode = m.mode
}

func WithAnyMulticastInterface() MulticastOption {
	return MulticastInterfaceModeOpt{mode: MulticastAnyInterface}
}

func WithAllMulticastInterface() MulticastOption {
	return MulticastInterfaceModeOpt{mode: MulticastAllInterface}
}

type MulticastInterfaceOpt struct {
	iface net.Interface
}

func (m MulticastInterfaceOpt) applyMC(o *MulticastOptions) {
	o.Iface = m.iface
	o.IFaceMode = MulticastSpecificInterface
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
