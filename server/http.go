package server

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"text/template"

	"github.com/aykevl93/plaincast/apps"
	"github.com/aykevl93/plaincast/apps/youtube"
)

// This implements a UPnP/DIAL server.
// DIAL is deprecated, but it's still being used by the YouTube app on Android.

// UPnP device description template
const DEVICE_DESCRIPTION = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0" configId="{{.ConfigId}}">
	<specVersion>
		<major>1</major>
		<minor>1</minor>
	</specVersion>
	<device>
		<deviceType>urn:schemas-upnp-org:device:dial:1</deviceType>
		<friendlyName>{{.FriendlyName}}</friendlyName>
		<manufacturer>-</manufacturer>
		<modelDescription>Play the audio of YouTube videos</modelDescription>
		<modelName>{{.ModelName}}</modelName>
		<modelNumber>{{.ModelNumber}}</modelNumber>
		<UDN>uuid:{{.DeviceUUID}}</UDN>
		<serviceList>
			<service>
				<serviceType>urn:schemas-upnp-org:service:dail:1</serviceType>
				<serviceId>urn:upnp-org:serviceId:dail</serviceId>
				<SCPDURL>/upnp/notfound</SCPDURL>
				<controlURL>/upnp/notfound</controlURL>
				<eventSubURL></eventSubURL>
			</service>
		</serviceList>
	</device>
</root>
`

// DIAL app template
const APP_RESPONSE = `<?xml version="1.0" encoding="UTF-8"?>
<service xmlns="urn:dial-multiscreen-org:schemas:dial" dialVer="1.7">
	<name>{{.name}}</name> 
	<options allowStop="false"/> 
	<state>{{.state}}</state> 
{{if .runningUrl}}
	<link rel="run" href="{{.runningUrl}}"/>
{{end}}
</service>
`

const HOME_TEMPLATE = `<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml">
<head>
<title>{{.Title}}server</title>
<meta name="viewport" content="width=device-width; initial-scale=1"/>
</head>
<body>
<h1>{{.Title}}</h1>
Apps:
<ul>
{{range .Apps}}
	<li>{{.Name}} {{if .Running}}(running){{end}}</li>
{{end}}
</ul>
</body>
</html>
`

type UPnPServer struct {
	descriptionTemplate *template.Template
	appStateTemplate    *template.Template
	homeTemplate        *template.Template
	httpPort            int
	apps                map[string]apps.App
	friendlyName        string
	appMatchString      *regexp.Regexp
	proxyClient         *http.Client
}

func NewUPnPServer() *UPnPServer {
	us := &UPnPServer{}

	us.appMatchString = regexp.MustCompile("^/apps/([a-zA-Z]+)(/run)?$")
	hostname, err := os.Hostname()
	if err != nil {
		panic(err)
	}
	us.friendlyName = FRIENDLY_NAME + " " + hostname

	// initialize all known apps
	us.apps = make(map[string]apps.App)
	us.apps["YouTube"] = youtube.New(FRIENDLY_NAME)

	// http Client as used by the proxy
	us.proxyClient = &http.Client{}

	http.HandleFunc("/upnp/description.xml", us.serveDescription)
	http.HandleFunc("/apps/", us.serveApp)
	http.HandleFunc("/proxy/", us.serveProxy)
	http.HandleFunc("/hacks/silence.wav", us.serveSilence)
	http.HandleFunc("/", us.serveHome)

	return us
}

func (us *UPnPServer) startServing() (int, error) {
	if us.httpPort != 0 {
		return 0, errors.New("already serving")
	}

	httpPort, err := serveZeroHTTPPort(nil)
	if err != nil {
		return 0, err
	}

	us.httpPort = httpPort

	return us.httpPort, nil
}

func (us *UPnPServer) serveHome(w http.ResponseWriter, req *http.Request) {
	log.Println("http", req.Method, req.URL.Path)

	if req.URL.Path != "/" {
		http.NotFound(w, req)
		return
	}

	w.Header().Set("Content-Type", "application/xhtml+xml; charset=utf-8")

	if us.homeTemplate == nil {
		tmpl, err := template.New("").Parse(HOME_TEMPLATE)
		if err != nil {
			// this shouldn't happen
			panic(err)
		}
		us.homeTemplate = tmpl
	}

	appNames := make([]string, len(us.apps))
	i := 0
	for name, _ := range us.apps {
		appNames[i] = name
		i++
	}
	sort.Strings(appNames)

	apps := make([]struct {
		Name    string
		Running bool
	}, len(us.apps))
	for i, name := range appNames {
		apps[i].Name = us.apps[name].FriendlyName()
		apps[i].Running = us.apps[name].Running()
	}

	err := us.homeTemplate.Execute(w, map[string]interface{}{
		"Title": us.friendlyName,
		"Apps":  apps,
	})
	if err != nil {
		// this shouldn't happen
		panic(err)
	}
}

// serveDescription serves the UPnP device description
func (us *UPnPServer) serveDescription(w http.ResponseWriter, req *http.Request) {
	log.Println("http", req.Method, req.URL.Path)

	w.Header().Set("Application-URL", fmt.Sprintf("http://%s:%d/apps/", getUrlIP(getLocalAddr(req)), us.httpPort))

	deviceDescription := map[string]interface{}{
		"ConfigId":     CONFIGID,
		"FriendlyName": us.friendlyName,
		"ModelName":    NAME,
		"ModelNumber":  VERSION,
		"DeviceUUID":   deviceUUID,
	}

	if us.descriptionTemplate == nil {
		tmpl, err := template.New("").Parse(DEVICE_DESCRIPTION)
		if err != nil {
			panic(err)
		}
		us.descriptionTemplate = tmpl
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	err := us.descriptionTemplate.Execute(w, deviceDescription)
	if err != nil {
		panic(err)
	}
}

// serveApp serves an app description and handles starting/stopping of apps
func (us *UPnPServer) serveApp(w http.ResponseWriter, req *http.Request) {
	log.Println("http", req.Method, req.URL.Path)

	matches := us.appMatchString.FindSubmatch([]byte(req.URL.Path))
	if matches == nil || len(matches) < 3 {
		http.NotFound(w, req)
		return
	}

	appName := string(matches[1])

	app, ok := us.apps[appName]
	if !ok {
		http.NotFound(w, req)
		return
	}

	if len(matches[2]) > 0 {
		if req.Method != "DELETE" {
			panic("expected delete on '" + req.URL.Path + "', not " + req.Method)
		}
		// This is a hidden feature. It is not advertized, but still supported,
		// to make it easy to re-enable the DELETE method.
		app.Quit()
		return
	}

	switch req.Method {
	case "GET":
	case "POST":
		length, err := strconv.Atoi(req.Header["Content-Length"][0])
		if err != nil {
			panic(err)
		}

		buf := make([]byte, length)
		_, err = io.ReadFull(req.Body, buf)
		if err != nil {
			panic(err)
		}
		message := string(buf)

		app.Start(message)

		w.WriteHeader(201)
		laddr := getLocalAddr(req)
		runningAppUrl := fmt.Sprintf("http://%s:%d/apps/%s/run", getUrlIP(laddr), us.httpPort, appName)
		w.Header().Set("Location", runningAppUrl)
		w.Header().Set("Content-Length", "0")
		return

	default:
		panic("unimplemented method: " + req.Method)
	}

	status := "stopped"
	runningUrl := ""
	if app.Running() {
		status = "running"
		runningUrl = "run"
	}

	if us.appStateTemplate == nil {
		tmpl, err := template.New("").Parse(APP_RESPONSE)
		if err != nil {
			panic(err)
		}
		us.appStateTemplate = tmpl
	}

	appResponse := map[string]interface{}{
		"name":       appName,
		"state":      status,
		"runningUrl": runningUrl,
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	err := us.appStateTemplate.Execute(w, appResponse)
	if err != nil {
		panic(err)
	}
}

// serveProxy is a simple proxy that is being used by the mplayer2 player
// backend, because it doesn't support SSL.
func (us *UPnPServer) serveProxy(w http.ResponseWriter, req *http.Request) {
	log.Println("http", req.Method, req.URL.Path)

	proxyUrl := req.URL.Path
	if req.URL.RawQuery != "" {
		proxyUrl += "?" + req.URL.RawQuery
	}
	proxyUrl = "https://" + proxyUrl[len("/proxy/"):]
	log.Println("proxy", proxyUrl)

	// client/proxied request
	creq, err := http.NewRequest("GET", proxyUrl, nil)
	if err != nil {
		panic(err)
	}
	for key, values := range req.Header {
		if key == "Host" {
			continue
		}
		for _, value := range values {
			creq.Header.Add(key, value)
		}
	}

	resp, err := us.proxyClient.Do(creq)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	if resp.ContentLength >= 0 {
		_, err = io.CopyN(w, resp.Body, resp.ContentLength)
	} else {
		_, err = io.Copy(w, resp.Body)
	}
	if err != nil {
		log.Println("http proxy error:", err)
	}
}

// serveSilence serves a WAV file of one byte silence
// This hack is needed for working aroud a VLC bug.
func (us *UPnPServer) serveSilence(w http.ResponseWriter, req *http.Request) {
	log.Println("http", req.Method, req.URL.Path)

	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", "46")
	io.WriteString(w, "RIFF&\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00D\xac\x00\x00\x88X\x01\x00\x02\x00\x10\x00data\x02\x00\x00\x00\x01\x00")
}

// partially copied from net/http sources
func serveZeroHTTPPort(handler http.Handler) (int, error) {
	// TODO: use a random port by binding to port 0.
	// Any port can be used by DIAL.
	server := &http.Server{Addr: ":8008", Handler: handler}

	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return 0, err
	}

	httpPort := ln.Addr().(*net.TCPAddr).Port
	server.Addr = ":" + strconv.Itoa(httpPort)

	go func() {
		err := server.Serve(ln.(*net.TCPListener))
		// should only be reachable in case of an error
		panic(err)
	}()

	return httpPort, nil
}
