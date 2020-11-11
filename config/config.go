package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sync"
)

type Config struct {
	path          string
	dataMutex     sync.Mutex
	data          map[string]interface{}
	saveChanMutex sync.Mutex
	saveChan      chan struct{}
}

var config *Config
var configLock sync.Mutex

const CONFIG_FILENAME = ".config/plaincast.json"

var disableConfig = flag.Bool("no-config", false, "Disable reading from and writing to config file")
var configPath = flag.String("config", "", "Config file location (default "+CONFIG_FILENAME+")")

// Get returns a global Config instance.
// It may be called multiple times: the same object will be returned each time.
func Get() *Config {
	configLock.Lock()
	defer configLock.Unlock()

	if config == nil {
		var path = ""

		if *disableConfig {
			// don't set config path

		} else if *configPath != "" {
			// set custom config path
			path = *configPath

		} else {
			// use default config path
			u, err := user.Current()
			handle(err, "could not get current user")

			path = filepath.Join(u.HomeDir, CONFIG_FILENAME)

			err = os.MkdirAll(filepath.Dir(path), 0777)
			handle(err, "could not create parent directories of config file")
		}

		config = newConfig(path)
	}

	return config
}

func newConfig(path string) *Config {
	c := &Config{}
	c.data = make(map[string]interface{})
	c.saveChan = make(chan struct{}, 1)

	if path == "" {
		return c
	}

	c.path = path

	if _, err := os.Stat(c.path); !os.IsNotExist(err) {
		f, err := os.Open(c.path)
		handle(err, "could not open config file")
		defer f.Close()

		buf, err := ioutil.ReadAll(f)
		handle(err, "could not read config file")
		handle(json.Unmarshal(buf, &c.data), "could not decode config file")
	}

	go c.saveTask()

	runtime.SetFinalizer(c, func(c *Config) {
		// Close the channel and exit the goroutine.
		close(c.saveChan)
	})

	return c
}

func (c *Config) Get(key string, valueCall func() (interface{}, error)) (interface{}, error) {
	c.dataMutex.Lock()
	defer c.dataMutex.Unlock()

	if value, ok := c.data[key]; ok {
		return value, nil
	}

	value, err := valueCall()
	if err != nil {
		return nil, err
	}

	c.data[key] = value
	c.save()

	return value, nil
}

func (c *Config) GetString(key string, valueCall func() (string, error)) (string, error) {
	c.dataMutex.Lock()
	defer c.dataMutex.Unlock()

	if value, ok := c.data[key]; ok {
		if svalue, ok := value.(string); ok {
			return svalue, nil
		} else {
			return "", errors.New("config value for key " + key + " is not a string")
		}
	}

	value, err := valueCall()
	if err != nil {
		return "", err
	}

	c.data[key] = value
	c.save()

	return value, nil
}

func (c *Config) GetInt(key string, valueCall func() (int, error)) (int, error) {
	c.dataMutex.Lock()
	defer c.dataMutex.Unlock()

	if value, ok := c.data[key]; ok {
		if svalue, ok := value.(float64); ok {
			return int(svalue), nil
		} else {
			return 0, errors.New("config value for key " + key + " is not an int")
		}
	}

	value, err := valueCall()
	if err != nil {
		return 0, err
	}

	c.data[key] = float64(value)
	c.save()

	return value, err
}

func (c *Config) Set(key string, value interface{}) {
	c.dataMutex.Lock()
	defer c.dataMutex.Unlock()

	c.data[key] = value
	c.save()
}

func (c *Config) SetInt(key string, value int) {
	c.dataMutex.Lock()
	defer c.dataMutex.Unlock()

	c.data[key] = float64(value)
	c.save()
}

func (c *Config) save() {
	if *disableConfig {
		return
	}

	// Make sure this function cannot be executed multiple times at the same
	// moment.
	c.saveChanMutex.Lock()
	defer c.saveChanMutex.Unlock()

	// Read a value from the channel if it exists. This will not block due to
	// the 'default' case.
	// This will only read a value in the (very) rare case that saveTask is
	// still busy with the previous save or just hasn't come to saving while
	// save() is called twice.
	select {
	case _ = <-c.saveChan:
	default:
	}

	// Send an empty value on the channel. This will not block as the 1-buffered
	// channel just got emptied.
	c.saveChan <- struct{}{}
}

// saveTask runs in a goroutine and handles saving the configuration
// asynchronously.
func (c *Config) saveTask() {
	for _ = range c.saveChan {
		data, err := json.MarshalIndent(&c.data, "", "\t")
		handle(err, "could not serialize config data")

		f, err := os.OpenFile(c.path+".tmp", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
		handle(err, "could not open config file")
		_, err = f.Write(data)
		handle(err, "could not write config file")
		handle(f.Close(), "could not close config file")

		handle(os.Rename(c.path+".tmp", c.path), "could not replace config file")
	}
}

func handle(err error, message string) {
	if err != nil {
		fmt.Printf("ERROR: %s: %s\n", message, err)
		os.Exit(1)
	}
}
