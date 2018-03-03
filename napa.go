package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sync"

	"./analyst"
	"./datastore"
	"./gdax"
	"./parse"
	"./trader"
	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
)

type listenLock struct {
	mu   *sync.Mutex
	todo bool
}

const (
	databaseDriver = "sqlite3"
	databaseName   = "./napa.db"
	databaseSQL    = "./napa.sql"
)

var (
	indexFileHTML []byte
	indexFileJS   []byte
)

func install() {
	fmt.Println("deleting database if exists")
	err := os.Remove(databaseName)
	if err != nil && !os.IsNotExist(err) {
		panic(err)
	}
	fmt.Println("creating database")
	db, err := sql.Open(databaseDriver, databaseName)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	err = datastore.RunFile(db, databaseSQL)
	if err != nil {
		panic(err)
	}
}

func server() {
	fmt.Println("loading files")
	file, err := os.Open("napa.html")
	if err != nil {
		panic(err)
	}
	indexFileHTML, err = ioutil.ReadAll(file)
	if err != nil {
		panic(err)
	}
	file, err = os.Open("napa.js")
	if err != nil {
		panic(err)
	}
	indexFileJS, err = ioutil.ReadAll(file)
	if err != nil {
		panic(err)
	}
	fmt.Println("listening and serving")
	http.HandleFunc("/", indexHTML)
	http.HandleFunc("/napa.js", indexJS)
	http.HandleFunc("/websocket", clientSocket)
	http.ListenAndServe(":80", nil)
}

func indexHTML(writer http.ResponseWriter, request *http.Request) {
	writer.Write(indexFileHTML)
}

func indexJS(writer http.ResponseWriter, request *http.Request) {
	writer.Write(indexFileJS)
}

func exchangeSocket(clientSocket *websocket.Conn, lock *sync.Mutex, listen *listenLock) error {
	fmt.Println("connecting to exchange")
	url := "wss://ws-feed.gdax.com"
	connection, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	js := json.RawMessage(`{"type":"subscribe", "product_ids":["BTC-USD"], "channels":["ticker"]}`)
	err = connection.WriteJSON(js)
	if err != nil {
		return err
	}
	fmt.Println("listening to exchange")
	for {
		var proceed bool
		listen.mu.Lock()
		proceed = listen.todo
		listen.mu.Unlock()
		if !proceed {
			break
		}
		var js interface{}
		err := connection.ReadJSON(&js)
		if err != nil {
			fmt.Println(err)
			break
		}
		message, ok := js.(map[string]interface{})
		if !ok {
			continue
		}
		messageType, ok := message["type"].(string)
		if !ok {
			continue
		}
		if messageType == "ticker" {
			time, _ := message["time"].(string)
			productID, _ := message["product_id"].(string)
			price, _ := message["price"].(string)
			side, _ := message["side"].(string)
			clientMessage := fmt.Sprintf(`{"uid":"ticker", "time":"%s", "product_id":"%s", "price":"%s", "side":"%s"}`, time, productID, price, side)
			go clientWrite(clientSocket, lock, clientMessage) // broadcast close exchange socket if fail ?
		}
	}
	connection.Close()
	fmt.Println("exchange connection closed")
	return nil
}

func clientWrite(connection *websocket.Conn, lock *sync.Mutex, rawJs string) {
	js := json.RawMessage([]byte(rawJs))
	lock.Lock()
	err := connection.WriteJSON(js)
	lock.Unlock()
	fmt.Println("sent", rawJs)
	if err != nil {
		fmt.Println(err)
	}
}

func clientRead(connection *websocket.Conn) {
	var lock = &sync.Mutex{}
	var exchangeLock = &listenLock{}
	exchangeLock.mu = &sync.Mutex{}
	exchangeLock.todo = false
	for {
		var js interface{}
		err := connection.ReadJSON(&js)
		if err != nil {
			fmt.Println(err)
			connection.Close()
			break
		}
		message, ok := js.(map[string]interface{})
		if !ok {
			continue
		}
		uid, ok := message["uid"].(string)
		if !ok {
			continue
		}
		switch uid {
		case "sub-exchange":
			var current bool
			exchangeLock.mu.Lock()
			current = exchangeLock.todo
			exchangeLock.todo = true
			exchangeLock.mu.Unlock()
			if current {
				continue
			}
			go clientWrite(connection, lock, `{"uid":"log", "message":"subbing to exchange"}`)
			go exchangeSocket(connection, lock, exchangeLock)
		case "unsub-exchange":
			go clientWrite(connection, lock, `{"uid":"log", "message":"unsubbing from exchange"}`)
			exchangeLock.mu.Lock()
			exchangeLock.todo = false
			exchangeLock.mu.Unlock()
		}
	}
}

func clientSocket(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Origin") != "http://"+request.Host {
		http.Error(writer, "origin not allowed", 403)
		return
	}
	upgrader := websocket.Upgrader{}
	connection, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		http.Error(writer, "could not open websocket", 400)
		return
	}
	go clientRead(connection)
}

func getFile(path string) (map[string]interface{}, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	contents, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, err
	}
	var decode interface{}
	err = json.Unmarshal(contents, &decode)
	if err != nil {
		return nil, err
	}
	js, _ := decode.(map[string]interface{})
	return js, nil
}

func main() {
	fmt.Println("napa bot")

	if len(os.Args) > 1 {
		if os.Args[1] == "install" {
			install()
			return
		}
		if os.Args[1] == "server" {
			server()
			return
		}
	}

	// load files
	public, err := getFile("./public.json")
	if err != nil {
		panic(err)
	}

	private, err := getFile("../private.json")
	if err != nil {
		panic(err)
	}

	products := []string{"LTC-USD"} // "BTC-USD", "ETH-USD",

	settings := &analyst.Settings{}
	settings.TimeInterval = parse.Integer(public, "interval")
	settings.EmaShort = parse.Integer(public, "ema-short")
	settings.EmaLong = parse.Integer(public, "ema-long")
	settings.RsiPeriods = parse.Integer(public, "rsi")

	auth := &gdax.Authentication{}
	auth.Key = parse.Text(private, "key")
	auth.Secret = parse.Text(private, "secret")
	auth.Passphrase = parse.Text(private, "phrase")

	// database
	db, err := sql.Open(databaseDriver, databaseName)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// connect to exchange
	messages := make(chan interface{})
	channels := []string{"ticker"} //, "level2"}
	go gdax.ExchangeSocket(products, channels, messages)

	poll := &gdax.Poll{}
	poll.OrderTime = 2
	poll.HistoryTime = 4
	go gdax.Polling(auth, poll, messages)

	trader.Run(db, auth, products, settings, messages)
}
