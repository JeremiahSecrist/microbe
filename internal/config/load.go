package config

import "fmt"

func Load(path string) (*Compose, error) {
	return nil, fmt.Errorf("config: not implemented yet")
}

func (c *Compose) Validate() error {
	return fmt.Errorf("config: validation not implemented yet")
}
