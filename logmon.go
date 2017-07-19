package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/takeshy/tail"
)

// Monitor is whole struct include some of Watching.
type Monitor struct {
	Path            string
	WaitMillisecond int64
	Rule            []Watching
}

// Watching expresse as a rule of monitor.
type Watching struct {
	Path            string
	Target          *regexp.Regexp
	Ignore          *regexp.Regexp
	WaitMillisecond int64
	Command         string
}

func readConf(path string) string {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		panic(err)
	}

	return string(data)
}

func parseConf(contentStr string) []Watching {
	contents := strings.Split(contentStr, "\n")
	ret := []Watching{}
	fileRe := regexp.MustCompile("^:(.*)")
	targetRe := regexp.MustCompile("^\\((.*)\\)$")
	ignoreRe := regexp.MustCompile("^\\[(.*)\\]$")
	timeRe := regexp.MustCompile("^{(.*)}$")
	commandRe := regexp.MustCompile("^[^#].*")
	var path string
	var target, ignore *regexp.Regexp
	var waitMillisecond int64
	for i := 0; i < len(contents); i++ {
		if fileRe.MatchString(contents[i]) {
			if target != nil || ignore != nil {
				panic(strconv.Itoa(i) + ":format error")
			}
			path = fileRe.ReplaceAllString(contents[i], "$1")
		} else if targetRe.MatchString(contents[i]) {
			if path == "" {
				panic(strconv.Itoa(i) + " target appear before path")
			}
			target = regexp.MustCompile(targetRe.ReplaceAllString(contents[i], "$1"))
		} else if ignoreRe.MatchString(contents[i]) {
			if path == "" {
				panic(strconv.Itoa(i) + "ignore appear before path ")
			}
			ignore = regexp.MustCompile(ignoreRe.ReplaceAllString(contents[i], "$1"))
		} else if timeRe.MatchString(contents[i]) {
			if path == "" {
				panic(strconv.Itoa(i) + "time appear before path")
			}
			waitMillisecondStr := timeRe.ReplaceAllString(contents[i], "$1")
			milliSec, err := strconv.ParseInt(waitMillisecondStr, 10, 64)
			if err != nil {
				panic(err)
			}
			waitMillisecond = milliSec
		} else if commandRe.MatchString(contents[i]) {
			if path == "" || target == nil {
				panic(strconv.Itoa(i) + "command format error")
			}
			ret = append(ret, Watching{path, target, ignore, waitMillisecond, contents[i]})
			path = ""
			waitMillisecond = 0
			target = nil
			ignore = nil
		}
	}
	return ret
}

func escapeShell(s string) string {
	message := strings.Replace(s, "'", "\\047", -1)
	message = strings.Replace(message, "$", "\\044", -1)
	return message
}

func executeCommand(conf Watching, targetMessage string) {
	replaceRe := regexp.MustCompile("<%%%%>")
	command := replaceRe.ReplaceAllString(conf.Command, targetMessage)
	_, err := exec.Command("sh", "-c", command).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(-1)
	}
}

func printConf(conf Watching) {
	fmt.Printf("Logfile: %s\n", conf.Path)
	fmt.Printf("Message: %s\n", conf.Target)
	if conf.Ignore != nil {
		fmt.Printf("Ignore: %s\n", conf.Ignore)
	}
	if conf.WaitMillisecond != 0 {
		fmt.Printf("WaitMillisecond: %d\n", conf.WaitMillisecond)
	}
	fmt.Printf("Action: %s\n", conf.Command)
}

func logMonitor(conf Watching) {
	c := tail.Watch(conf.Path)
	var targetMessage string
	var mutex sync.Mutex
	for {
		select {
		case s := <-c:
			if targetMessage != "" {
				mutex.Lock()
				if targetMessage != "" {
					targetMessage += ("\n" + escapeShell(s))
				}
				mutex.Unlock()
			} else if conf.Target.MatchString(s) && (conf.Ignore == nil || !conf.Ignore.MatchString(s)) {
				targetMessage += escapeShell(s)
				if conf.WaitMillisecond == 0 {
					executeCommand(conf, targetMessage)
					targetMessage = ""
				} else {
					timer := time.NewTimer(time.Duration(conf.WaitMillisecond) * time.Millisecond)
					go func() {
						<-timer.C
						mutex.Lock()
						executeCommand(conf, targetMessage)
						targetMessage = ""
						mutex.Unlock()
					}()
				}
			}
		}
	}
}

func multiLogMonitor(mon Monitor) {
	c := tail.Watch(mon.Path)
	var targetMessage string
	var mutex sync.Mutex
	for {
		select {
		case s := <-c:
			for i := range mon.Rule {
				conf := mon.Rule[i]
				if targetMessage != "" {
					mutex.Lock()
					if targetMessage != "" {
						targetMessage += ("\n" + escapeShell(s))
					}
					mutex.Unlock()
				} else if conf.Target.MatchString(s) && (conf.Ignore == nil || !conf.Ignore.MatchString(s)) {
					targetMessage += escapeShell(s)
					if conf.WaitMillisecond == 0 {
						executeCommand(conf, targetMessage)
						targetMessage = ""
					} else {
						timer := time.NewTimer(time.Duration(conf.WaitMillisecond) * time.Millisecond)
						go func() {
							<-timer.C
							mutex.Lock()
							executeCommand(conf, targetMessage)
							targetMessage = ""
							mutex.Unlock()
						}()
					}
				}
			}
		}

	}
}

func convertToMonitor(confs []Watching) []Monitor {
	mons := []Monitor{}
	for i := range confs {
		mon := getOrCreateMonitor(mons, confs[i])
		mon.Rule = append(mon.Rule, confs[i])
		if !contains(mons, *mon) {
			mons = append(mons, *mon)
		}
	}
	return mons
}

func contains(mons []Monitor, mon Monitor) bool {
	for i := range mons {
		if mons[i].Path == mon.Path {
			return true
		}
	}
	return false
}

func getOrCreateMonitor(mons []Monitor, conf Watching) *Monitor {
	for i := range mons {
		if mons[i].Path == conf.Path {
			return &mons[i]
		}
	}
	mon := Monitor{}
	mon.Path = conf.Path
	mon.WaitMillisecond = conf.WaitMillisecond
	return &mon
}

func main() {
	conf := flag.String("f", "/etc/logmon/logmon.conf", "config file(Default: /etc/logmon/logmon.conf)")
	check := flag.Bool("c", false, "check config")
	flag.Parse()
	confs := parseConf(readConf(*conf))
	if *check {
		fmt.Printf("Config file: %s\n", *conf)
		for i := range confs {
			fmt.Printf("\n")
			printConf(confs[i])
		}
		return
	}

	mons := convertToMonitor(confs)
	for i := range mons {
		go multiLogMonitor(mons[i])
	}
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	_ = <-signalChan
}
