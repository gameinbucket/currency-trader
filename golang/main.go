package main

import (
	"container/list"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const (
	slash                 = string(os.PathSeparator)
	orderFile             = "." + slash + "orders.txt"
	orderBackupFile       = "." + slash + "orders_backup.txt"
	orderUpdateFile       = "." + slash + "orders_update.txt"
	orderUpdateBackupFile = "." + slash + "orders_update_backup.txt"
	fundsFile             = "." + slash + "funds.txt"
	fundsBackupFile       = "." + slash + "funds_backup.txt"
	fundsUpdateFile       = "." + slash + "funds_update.txt"
	fundsUpdateBackupFile = "." + slash + "funds_update_backup.txt"
	settingsFile          = "." + slash + "settings.txt"
	logFile               = "." + slash + "log.txt"
	privateFile           = ".." + slash + ".." + slash + "private.txt"
)

var (
	interrupt = false
	product   string
	auth      map[string]string
	algo      *macd
	orders    = list.New()
	funds     = make(map[string]*currency)
)

func main() {
	fmt.Println("napa bot")
	signals()
	logging()
	auth = readMap(privateFile)
	initOrders()
	initFunds()
	p, granularity, granularityInt, emaShort, emaLong := initSettings()
	product = p
	interval := time.Second * time.Duration(granularityInt)
	offset := -interval * time.Duration(emaLong)
	sleeping := time.Second * time.Duration(2)
	candleTime := int64(0)
	regulate := true
	updateText := ""
	for {
		if interrupt {
			break
		}
		end := time.Now().UTC()
		start := end.Add(offset)
		printing := fmt.Sprint("* ", product, " | ", start.Format(time.Stamp), " -> ", end.Format(time.Stamp))
		candles, err := candles(product, start.Format(time.RFC3339), end.Format(time.RFC3339), granularity)
		if err != nil {
			logger(err.Error())
			time.Sleep(time.Second)
			continue
		}
		size := len(candles)
		var waitTil time.Time
		if size > 0 && candles[size-1].time > candleTime {
			algo = newMacd(emaShort, emaLong, candles[0].closing)
			candleTime = candles[size-1].time
			for i := 1; i < size; i++ {
				algo.update(candles[i].closing)
			}
			updateText = fmt.Sprint(" | MACD ", strconv.FormatFloat(algo.current, 'f', 3, 64), " | SIGNAL ", algo.signal)
			process()
			if regulate {
				wait := interval - time.Now().Sub(time.Unix(candles[size-1].time, 0))
				if wait < 0 {
					waitTil = time.Now().Add(interval)
				} else {
					waitTil = time.Now().Add(wait)
				}
				regulate = false
			} else {
				waitTil = time.Now().Add(interval)
			}
		} else {
			waitTil = time.Now().Add(time.Second * time.Duration(6))
		}
		printing += updateText + fmt.Sprint(" | SLEEPING -> ", waitTil.Format(time.Stamp), " *")
		fmt.Println(printing)
		for time.Now().Before(waitTil) {
			if interrupt {
				break
			}
			<-time.NewTimer(sleeping).C
		}
	}
}

func initOrders() {
	o := readList(orderFile)
	for i := 0; i < len(o); i++ {
		fmt.Println("fetching order:", o[i])
		order, status, err := readOrder(auth, o[i])
		if order == nil && err == nil {
			err = errors.New("order is null | status code " + strconv.FormatInt(int64(status), 10))
		}
		if err != nil {
			logger(err.Error())
			panic(err)
		}
		fmt.Println(order)
		orders.PushBack(order)
	}
}

func initSettings() (string, string, int64, int64, int64) {
	s := readMap(settingsFile)
	granularity := s["granularity"]
	granularityInt, err := strconv.ParseInt(granularity, 10, 64)
	if err != nil {
		logger(err.Error())
		panic(err)
	}
	emaShort, err := strconv.ParseInt(s["ema-short"], 10, 64)
	if err != nil {
		logger(err.Error())
		panic(err)
	}
	emaLong, err := strconv.ParseInt(s["ema-long"], 10, 64)
	if err != nil {
		logger(err.Error())
		panic(err)
	}
	return s["product"], granularity, granularityInt, emaShort, emaLong
}

func initFunds() {
	f := readMap(fundsFile)
	for key, value := range f {
		funds[key] = newCurrency(value)
	}
}

func signals() {
	s := make(chan os.Signal)
	signal.Notify(s, syscall.SIGINT, syscall.SIGTERM)
	go (func() {
		<-s
		interrupt = true
		fmt.Println("\nsignal interrupt")
	})()
}

func logging() {
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		logger("failed to open log file:", logFile)
		os.Exit(0)
	}
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(file)
}

func logger(s ...string) {
	log.Println(s)
	fmt.Println(s)
}
