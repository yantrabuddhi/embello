package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/tarm/serial"
	"gopkg.in/readline.v1"
)

var (
	rlInstance *readline.Instance
	conn       *serial.Port
	serIn      = make(chan []byte)
	outBound   = make(chan string)
	progress   = make(chan bool)
	incLevel   = make(chan int)
)

func main() {
	var err error
	rlInstance, err = readline.NewEx(&readline.Config{
		UniqueEditLine: true,
	})
	check(err)
	defer rlInstance.Close()

	tty := "/dev/cu.SLAB_USBtoUART"
	conn, err = serial.OpenPort(&serial.Config{Name: tty, Baud: 115200})
	check(err)

	go serialInput()
	go serialExchange()

	outBound <- ""
	<-progress
	for {
		line, err := rlInstance.Readline()
		if err != nil { // io.EOF, readline.ErrInterrupt
			break
		}
		if strings.HasPrefix(line, "include ") {
			doInclude(line[8:])
		} else {
			outBound <- line
			<-progress
		}
	}
}

func check(err error) {
	if err != nil {
		if rlInstance != nil {
			rlInstance.Close()
		}
		log.Fatal(err)
	}
}

func serialInput() {
	buf := make([]byte, 100)
	for {
		n, err := conn.Read(buf)
		check(err)
		if n == 0 {
			close(serIn)
			return
		}
		serIn <- buf[:n]
	}
}

func readWithTimeout() []byte {
	select {
	case data := <-serIn:
		return data
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}

func serialExchange() {
	includeDepth := 0
	for {
		select {
		case data := <-serIn:
			if len(data) == 0 {
				return
			}
			print(string(data))
		case line := <-outBound:
			// the task here is to omit "normal" output for included lines,
			// i.e. lines which only generate an echo, a space, and " ok.\n"
			// everything else should be echoed in full, including the input
			including := includeDepth > 0
			prompt := " ok.\n"
			if len(line) > 0 {
				serialSend(line)
				prefix, matched := expectEcho(line, func (s string) {
					print(s)
				})
				print(prefix)
				if matched && !including {
					print(line)
					line = ""
				}
			}
			// now that the echo is done, send a CR and wait for the prompt
			serialSend("\r")
			prefix, matched := expectEcho(prompt, func (s string) {
				print(line + s)
				line = ""
			})
			if !matched {
				prompt = ""
			}
			if !including || prefix != " " || !matched {
				print(line + prefix + prompt)
			}
			// signal to sender that this request has been processed
			progress <- matched
		case n := <-incLevel:
			includeDepth += n
		}
	}
}

func expectEcho(match string, overflow func(string)) (string, bool) {
	var collected []byte
	for {
		data := readWithTimeout()
		if len(data) == 0 {
			return string(collected), false
		}
		collected = append(collected, data...)
		if bytes.HasSuffix(collected, []byte(match)) {
			bytesBefore := len(collected) - len(match)
			return string(collected[:bytesBefore]), true
		}
		if n := bytes.LastIndexByte(collected, '\n'); n >= 0 {
			overflow(string(collected[:n+1]))
			collected = collected[n+1:]
		}
	}
}

func serialSend(data string) {
	_, err := conn.Write([]byte(data))
	check(err)
}

func doInclude(fname string) {
	incLevel <- +1
	defer func() { incLevel <- -1 }()

	lineNum := 0
	fmt.Printf("\t>>> include %s\n", fname)
	defer func() {
		fmt.Printf("\t<<<<<<<<<<< %s (%d lines)\n", fname, lineNum)
	}()

	f, err := os.Open(fname)
	if err != nil {
		fmt.Println(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		lineNum++

		s := strings.TrimLeft(line, " ")
		if s == "" || strings.HasPrefix(s, "\\ ") {
			continue // don't send empty or comment-only lines
		}

		outBound <- line
		<-progress
	}
}
