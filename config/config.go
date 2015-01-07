package config

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"sync"
)

type Config struct {
	sync.Mutex
	data map[string]interface{}
	path string
}

var config *Config

const CONFIG_FILENAME = ".config/plaincast.json"

// Get returns a global Config instance.
// It may be called multiple times: the same object will be returned each time.
func Get() *Config {
	if config == nil {
		u, err := user.Current()
		handle(err, "could not get current user")

		path := filepath.Join(u.HomeDir, CONFIG_FILENAME)

		err = os.MkdirAll(filepath.Dir(path), 0777)
		handle(err, "could not create parent directories of config file")

		config = newConfig(path)
	}

	return config
}

func newConfig(path string) *Config {
	c := Config{}
	c.data = make(map[string]interface{})
	c.path = path

	if _, err := os.Stat(c.path); !os.IsNotExist(err) {
		f, err := os.Open(c.path)
		handle(err, "could not open config file")
		defer f.Close()

		buf, err := ioutil.ReadAll(f)
		handle(err, "could not read config file")
		handle(json.Unmarshal(buf, &c.data), "could not decode config file")
	}

	return &c
}

func (c *Config) GetString(key string, valueCall func() (string, error)) (string, error) {
	c.Lock()
	defer c.Unlock()

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
	c.Lock()
	defer c.Unlock()

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

func (c *Config) SetInt(key string, value int) {
	c.Lock()
	defer c.Unlock()

	c.data[key] = float64(value)
	c.save()
}

func (c *Config) save() {
	// TODO do saving asynchronously

	data, err := json.MarshalIndent(&c.data, "", "\t")
	handle(err, "could not serialize config data")

	f, err := os.Create(c.path + ".tmp")
	handle(err, "could not open config file")
	_, err = f.Write(data)
	handle(err, "could not write config file")
	handle(f.Close(), "could not close config file")

	handle(os.Rename(c.path+".tmp", c.path), "could not replace config file")
}

func handle(err error, message string) {
	if err != nil {
		log.Fatalf("%s: %s\n", message, err)
	}
}
