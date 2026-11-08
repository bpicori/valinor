package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultInstanceName is the instance backed by ~/.valinor.
	DefaultInstanceName = "default"
	// InstancesDirName is the directory holding named instance roots.
	InstancesDirName = "instances"
)

// Instance describes one Valinor instance root and its config file.
type Instance struct {
	Name       string
	Root       string
	ConfigPath string
}

// ResolveInstance maps an instance name to its root and config path.
func ResolveInstance(name string) (Instance, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultInstanceName
	}
	if err := ValidateInstanceName(name); err != nil {
		return Instance{}, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Instance{}, fmt.Errorf("resolve home directory: %w", err)
	}
	base := filepath.Join(home, ".valinor")
	root := base
	if name != DefaultInstanceName {
		root = filepath.Join(base, InstancesDirName, name)
	}
	return Instance{
		Name:       name,
		Root:       root,
		ConfigPath: filepath.Join(root, ConfigFileName),
	}, nil
}

// ValidateInstanceName accepts lowercase names that are safe for paths and
// systemd template instances.
func ValidateInstanceName(name string) error {
	if name == "" {
		return fmt.Errorf("instance name is required")
	}
	if len(name) > 32 {
		return fmt.Errorf("instance name must be at most 32 characters")
	}
	for i, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !valid {
			return fmt.Errorf("instance name %q must use lowercase letters, digits, '-' or '_'", name)
		}
		if i == 0 && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return fmt.Errorf("instance name must start with a letter or digit")
		}
	}
	return nil
}

// LoadForInstance loads and validates one instance config.
func LoadForInstance(name string) (Config, error) {
	instance, err := ResolveInstance(name)
	if err != nil {
		return Config{}, err
	}
	if _, err := os.Stat(instance.ConfigPath); err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("%w: %s", ErrNotFound, instance.ConfigPath)
		}
		return Config{}, err
	}
	return loadFromFile(instance.ConfigPath, instance.Root, instance.Name != DefaultInstanceName)
}

// LoadInitialForInstance loads an existing instance config or returns wizard defaults.
func LoadInitialForInstance(name string) (Config, error) {
	instance, err := ResolveInstance(name)
	if err != nil {
		return Config{}, err
	}
	if _, err := os.Stat(instance.ConfigPath); err == nil {
		return loadFromFile(instance.ConfigPath, instance.Root, instance.Name != DefaultInstanceName)
	} else if !os.IsNotExist(err) {
		return Config{}, err
	}
	return LoadWizardTemplateForInstance(name)
}

// LoadWizardTemplateForInstance returns wizard defaults rooted at the instance.
func LoadWizardTemplateForInstance(name string) (Config, error) {
	instance, err := ResolveInstance(name)
	if err != nil {
		return Config{}, err
	}
	cfg, err := LoadWizardTemplate()
	if err != nil {
		return Config{}, err
	}
	cfg.ValinorDir = instance.Root
	cfg.GatewayPort = NextGatewayPort()
	return cfg, nil
}

// ListInstances returns the default instance when configured and every named
// instance with a config file.
func ListInstances() ([]Instance, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	base := filepath.Join(home, ".valinor")
	defaultInstance, err := ResolveInstance(DefaultInstanceName)
	if err != nil {
		return nil, err
	}

	instances := make([]Instance, 0, 4)
	if _, err := os.Stat(defaultInstance.ConfigPath); err == nil {
		instances = append(instances, defaultInstance)
	}
	entries, err := os.ReadDir(filepath.Join(base, InstancesDirName))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		instance, err := ResolveInstance(entry.Name())
		if err != nil {
			continue
		}
		if _, err := os.Stat(instance.ConfigPath); err != nil {
			continue
		}
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].Name == DefaultInstanceName {
			return true
		}
		if instances[j].Name == DefaultInstanceName {
			return false
		}
		return instances[i].Name < instances[j].Name
	})
	return instances, nil
}

// NextGatewayPort returns the first free gateway port not already configured
// by another instance. It also verifies the port is currently bindable.
func NextGatewayPort() int {
	used := configuredGatewayPorts()
	for port := DefaultGatewayPort; port < DefaultGatewayPort+100; port++ {
		if used[port] || !portAvailable(port) {
			continue
		}
		return port
	}
	return DefaultGatewayPort
}

func configuredGatewayPorts() map[int]bool {
	ports := make(map[int]bool)
	instances, err := ListInstances()
	if err != nil {
		return ports
	}
	for _, instance := range instances {
		cfg, err := LoadForInstance(instance.Name)
		if err != nil {
			continue
		}
		if cfg.GatewayPort > 0 {
			ports[cfg.GatewayPort] = true
		}
	}
	return ports
}

func portAvailable(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	time.Sleep(time.Millisecond) // allow the kernel to release the probe socket
	return true
}
