package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// LogEntry is single parsed entry from the log file
type LogEntry struct {
	Time   time.Time
	Method string
	URL    string
}

// LogReader provides generic log parser interface
type LogReader interface {
	Read() (*LogEntry, error)
}

var logChannel chan string
var logWg sync.WaitGroup
var httpWg sync.WaitGroup

func checkErr(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

var format string
var inputLogFile string
var logFile string
var prefix string
var inputFileType string
var ratio int64
var debug bool

func init() {
	flag.StringVar(&format, "format", `$remote_addr [$time_local] "$request" $status $request_length $body_bytes_sent $request_time "$t_size" $read_time $gen_time`, "Nginx log format")
	flag.StringVar(&inputLogFile, "file", "-", "Log file name to read. Read from STDIN if file name is '-'")
	flag.StringVar(&logFile, "log", "-", "File to report timings to, default is stdout")
	flag.StringVar(&prefix, "prefix", "http://localhost", "URL prefix to query")
	flag.StringVar(&inputFileType, "file-type", "nginx", "Input log type (nginx or haproxy)")
	flag.Int64Var(&ratio, "ratio", 1, "Replay speed ratio, higher means faster replay speed")
	flag.BoolVar(&debug, "debug", false, "Print extra debugging information")

	logChannel = make(chan string)
}

func mainLoop(reader LogReader) {
	var nilTime time.Time
	var lastTime time.Time

	for {
		rec, err := reader.Read()

		if err == io.EOF {
			log.Println("Reached EOF")
			break
		} else {
			checkErr(err)
		}

		if rec.Method == "GET" {
			if lastTime != nilTime {
				differenceUnix := rec.Time.Sub(lastTime).Nanoseconds()

				if differenceUnix > 0 {
					durationWithRation := time.Duration(differenceUnix / ratio)

					if debug {
						log.Printf("Sleeping for: %.2f seconds", durationWithRation.Seconds())
					}
					time.Sleep(durationWithRation)
				} else {
					if debug {
						log.Println("No need for sleep!")
					}
				}

			}

			lastTime = rec.Time

			httpWg.Add(1)
			go fireHTTPRequest(rec.Method, rec.URL)
		}
	}
}

func fireHTTPRequest(method string, url string) {
	defer httpWg.Done()

	path := prefix + url

	if debug {
		log.Printf("Querying %s\n", path)
	}

	client := &http.Client{
		Timeout: time.Minute,
	}

	var logMessage string
	startTime := time.Now()
	startTS := startTime.Unix()

	req, err := http.NewRequest(method, path, nil)

	if err != nil {
		if debug {
			log.Printf("ERROR %s while creating new request to %s", err, path)
		}

		logMessage = fmt.Sprintf("%d\t%d\t%d\t%s\t%s\n", 500, startTS, 0, url, err)
		logChannel <- logMessage

		return
	}

	req.Header.Set("User-Agent", "Log Replay (github.com/Gonzih/log-replay)")

	resp, err := client.Do(req)
	endTime := time.Now()

	duration := endTime.Sub(startTime).Nanoseconds()

	if err != nil {
		if debug {
			log.Printf(`ERROR "%s" while querying "%s"`, err, path)
		}

		logMessage = fmt.Sprintf("%d\t%d\t%d\t%s\t%s\n", 500, startTS, duration, url, err)
	} else {
		status := resp.StatusCode
		logMessage = fmt.Sprintf("%d\t%d\t%d\t%s\n", status, startTS, duration, url)
	}

	logChannel <- logMessage
}

func logLoop() {
	defer logWg.Done()

	var writer io.Writer

	switch logFile {
	case "-":
		writer = os.Stdout
	default:
		file, err := os.Create(logFile)
		checkErr(err)
		defer file.Close()
		writer = file
	}

	for logMessage := range logChannel {
		_, err := io.WriteString(writer, logMessage)
		checkErr(err)
	}
}

func main() {
	flag.Parse()

	var inputReader io.Reader

	if debug {
		log.Printf("Parsing %s log file\n", inputLogFile)
		log.Printf("Using log type %s", inputFileType)
	}

	if inputLogFile == "dummy" {
		if inputFileType == "nginx" {
			inputReader = strings.NewReader(`89.234.89.123 [08/Nov/2013:13:39:18 +0000] "GET /t/100x100/foo/bar.jpeg HTTP/1.1" 200 1027 2430 0.014 "100x100" 10 1`)
		} else {
			inputReader = strings.NewReader(`<142>Sep 27 00:15:57 haproxy[28513]: 67.188.214.167:64531 [27/Sep/2013:00:15:43.494] frontend~ test/10.127.57.177-10000 449/0/0/13531/13980 200 13824 - - ---- 6/6/0/1/0 0/0 "GET / HTTP/1.1"`)
		}
	} else if inputLogFile == "-" {
		inputReader = os.Stdin
	} else {
		file, err := os.Open(inputLogFile)

		checkErr(err)
		defer file.Close()

		inputReader = file
	}

	var reader LogReader

	switch inputFileType {
	case "nginx":
		reader = NewNginxReader(inputReader, format)
	case "haproxy":
		reader = NewHaproxyReader(inputReader)
	default:
		log.Fatalf("file-type can be either haproxy or nginx, not '%s'", inputFileType)
	}

	logWg.Add(1)
	go logLoop()

	mainLoop(reader)

	if debug {
		log.Println("Waiting for all http goroutines to stop")
	}

	httpWg.Wait()
	close(logChannel)

	if debug {
		log.Println("Waiting for log goroutine to stop")
	}

	logWg.Wait()
}
