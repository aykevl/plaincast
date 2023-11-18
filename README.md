# Plaincast

**This project is unmaintained**. Feel free to fork it and take over maintainership if you're interested in using it.

This is a small [DIAL](http://www.dial-multiscreen.org) server that emulates
Chromecast-like devices, and implements the YouTube app. It only renders the
audio, not the video, so it is very lightweight and can run headless.

It can be used as media server, for example on the [Raspberry
Pi](http://www.raspberrypi.org/).

## Installation

I'm going to assume you're running Linux for this installation guide, preferably
Debian Jessie (or newer when their time comes). Debian before Jessie contains
too old versions of certain packages.

First, make sure you have the needed dependencies installed:

 *  golang 1.3 (1.1+ might also work, but 1.0 certainly doesn't)
 *  libmpv-dev
 *  youtube-dl (see also 'notes on youtube-dl' below)

These can be installed in one go under Debian Jessie:

    $ sudo apt-get install golang libmpv-dev youtube-dl

If you haven't already set up a Go workspace, create one now. Some people like
to set it to their home directory, but you can also set it to a separate
directory. In any case, set the environment variable `$GOROOT` to this path:

    $ mkdir golang
    $ cd golang
    $ export GOPATH="`pwd`"

Then get the required packages and compile:

    $ go get -u github.com/aykevl/plaincast
	
To run the server, you can run the executable `bin/plaincast` relative to your Go
workspace.

    $ bin/plaincast [OPTIONS]

or install it as service

	$ cd src/github.com/aykevl/plaincast
	$ make install

If you want to remove service `$ make remove`

Any browser that supports chromecast extension and Android phone with YouTube app 
(or possibly iPhone, but I haven't tested) on the same network should recognize
the server and it should be possible to play the audio of videos on it. 


### Manual service installation

Copy compiled binary file `plaincast` to `/usr/local/bin/` and create new user *plaincast* in group *audio* 

	$ useradd -s /bin/false -r -M plaincast -g audio
	
Create directory 

	$ mkdir -p /var/local/plaincast
	$ chown plaincast:audio /var/local/plaincast

Copy systemd unit file `plaincast.service` to `/etc/systemd/system/` and enable the service 

`$ systemctl enable plaincast`


## Options
	-h, -help	    	Prints help text and exit
	-ao-pcm		    	Write audio to a file, 48kHz stereo format S16
	-app		    	Name of the app to run on startup, no need to use 
                    	as currently is supported only YouTube	
	-cachedir	    	Cache directory location for youtube-dl
	-config		    	Location of the configuration file, path to to config
                    	(default location ~/.config/plaincast.json)
    -friendly-name  	Custom friendly name (default "Plaincast HOSTNAME")	
	-http-port	    	Custom http port (default 8008)
	-log-libmpv	    	Log output of libmpv
	-log-mpv	    	Log MPV wrapper output
	-log-player	    	Log media player messages
	-log-server	    	Log HTTP and SSDP server
	-log-youtube    	Log YouTube app
	-loglevel	    	Baseline loglevel (info, warn, err) (default "warn")
	-no-config	    	Disable reading from and writing to config file
	-no-sspd	    	Disable SSDP server   


### Snapcast support

You can easily write audio output to snapcast pipe using option

`-ao-pcm PATH-TO-SNAPFIFO` 


## Notes on youtube-dl

`youtube-dl` is often too old to be used for downloading YouTube streams. You
can try to run `youtube-dl -U`, but it may say that it won't update because it
has been installed via a package manager. To fix this, uninstall youtube-dl, and
install it via pip. The steps required depend on the version of Python in your
`$PATH` variable. Check it with:

    $ python --version

Install using pip for **Python 2** (usually version 2.7.x), on Debian stretch
and below:

    $ sudo apt-get remove youtube-dl
    $ sudo apt-get install python-pip
    $ sudo pip2 install youtube-dl

Install using pip3 for **Python 3** (version 3.x). Only required when you have
configured the `python` binary to point to Python 3, or maybe on newer versions
of Debian.

    $ sudo apt-get remove youtube-dl
    $ sudo apt-get install python3-pip
    $ sudo pip3 install youtube-dl

Afterwards, you can update youtube-dl using:

    $ sudo pip install --upgrade youtube-dl

Or for Python 3:

    $ sudo pip3 install --upgrade youtube-dl

It is advisable to run this regularly as it has to keep up with YouTube updates.
Certainly first try updating youtube-dl when plaincast stops working.


## Thanks

I would like to thank the creators of
[leapcast](https://github.com/dz0ny/leapcast). Leapcast is a Chromecast
emulator, which was essential in the process of reverse-engineering the YouTube
protocol and better understanding the DIAL protocol.
