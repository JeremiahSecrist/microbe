package hostnet

type Allocator struct{}

func (a *Allocator) Next(network, subnet string) (string, error) {
	return "", nil
}

func (a *Allocator) Release(network, ip string) error {
	return nil
}
