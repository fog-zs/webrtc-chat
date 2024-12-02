package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

type SignalingMessage struct {
	Type      string `json:"type"`
	TargetID  string `json:"target_id"`
	Request   string `json:"request"`
	Offer     string `json:"offer"`
	Answer    string `json:"answer"`
	Candidate string `json:"candidate"`
	ID        string `json:"id"`
}

type OfferMessage struct {
	Type     string `json:"type"`
	TargetID string `json:"target_id"`
	Offer    string `json:"offer"`
	ID       string `json:"id"`
}

type AnswerMessage struct {
	Type     string `json:"type"`
	TargetID string `json:"target_id"`
	Answer   string `json:"answer"`
	ID       string `json:"id"`
}

type CandidateMessage struct {
	Type      string `json:"type"`
	TargetID  string `json:"target_id"`
	Candidate string `json:"candidate"`
	ID        string `json:"id"`
}

type MessageHandlerRegistry struct {
	mu       sync.Mutex
	handlers []func(msg webrtc.DataChannelMessage)
}

func (r *MessageHandlerRegistry) AddHandler(handler func(msg webrtc.DataChannelMessage)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers = append(r.handlers, handler)
}

func (r *MessageHandlerRegistry) HandleMessage(msg webrtc.DataChannelMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, handler := range r.handlers {
		go handler(msg) // 各ハンドラーを非同期で実行
	}
}

func main() {
	var serverIP string
	var enableLogging bool
	var localPort int
	var remotePort int

	flag.StringVar(&serverIP, "server", "", "Signaling Server IP address")
	flag.IntVar(&localPort, "local", -1, "Port forwarding rule")
	flag.IntVar(&remotePort, "remote", -1, "Server address to forward messages")
	flag.BoolVar(&enableLogging, "log", false, "Enable logging")
	flag.Parse()

	if !enableLogging {
		log.SetOutput(io.Discard)
	}

	if serverIP == "" {
		serverIP = getServerIPFromConfig()
	}
	conn := connectToWebSocket(serverIP)
	defer conn.Close()

	clientID := uuid.New().String()
	peerConnection, dataChannel := setupWebRTC()
	defer peerConnection.Close()

	targetID := ""
	pendingCandidates := []*webrtc.ICECandidate{}

	setupPeerConnectionEventHandlers(peerConnection, conn, &targetID, &pendingCandidates, clientID)

	sendSignalingRequest(conn, clientID)
	registry := &MessageHandlerRegistry{}

	setupDataChannelEventHandlers(peerConnection, registry)

	if localPort != -1 {
		go startLocal(localPort, registry, dataChannel)
	}

	if remotePort != -1 {
		go startRemote(remotePort, registry, dataChannel)
	}

	go handleSignalingMessages(conn, peerConnection, dataChannel, &targetID, &pendingCandidates, clientID)
	go sendUserMessages(dataChannel)

	// Wait for the program to be interrupted or terminated
	select {}

}

func getServerIPFromConfig() string {
	configPath := "config.json"

	// Check if config file exists
	_, err := os.Stat(configPath)
	if os.IsNotExist(err) {
		// If config file doesn't exist, create it with default values
		defaultConfig := struct {
			ServerIP string `json:"server_ip"`
		}{
			ServerIP: "ws://localhost:8080",
		}

		file, err := os.Create(configPath)
		if err != nil {
			log.Fatal("Config file create error: ", err)
		}
		defer file.Close()

		err = json.NewEncoder(file).Encode(defaultConfig)
		if err != nil {
			log.Fatal("Config file encode error: ", err)
		}

		log.Printf("Created default config file: %s\n", configPath)
		return defaultConfig.ServerIP
	}

	// Read config file
	file, err := os.Open(configPath)
	if err != nil {
		log.Fatal("Config file open error: ", err)
	}
	defer file.Close()

	var config struct {
		ServerIP string `json:"server_ip"`
	}
	err = json.NewDecoder(file).Decode(&config)
	if err != nil {
		log.Fatal("Config file decode error: ", err)
	}

	return config.ServerIP
}

func startLocal(localPort int, registry *MessageHandlerRegistry, dataChannel *webrtc.DataChannel) {
	go func() {
		listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", localPort))
		if err != nil {
			log.Fatal("Port forwarding listen error: ", err)
		}
		defer listener.Close()

		for {
			conn, err := listener.Accept()
			log.Println("Accepted TCP connection")
			if err != nil {
				log.Fatal("Port forwarding accept error: ", err)
			}
			go handleLocalProxy(conn, registry, dataChannel)
		}
	}()
}

func handleLocalProxy(conn net.Conn, registry *MessageHandlerRegistry, dataChannel *webrtc.DataChannel) {
	defer conn.Close()
	buf := make([]byte, 4096)

	// TCP接続からDataChannelにデータを送信
	go func() {
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if err == io.EOF {
					log.Println("Reached end of TCP connection")
					return
				}
				log.Printf("TCP connection read error: %v", err)
				return
			}

			// TCPから受信したデータをDataChannelに送信
			if err := dataChannel.Send(buf[:n]); err != nil {
				log.Printf("Error sending data to DataChannel: %v", err)
				return
			}
			log.Printf("Forwarded %d bytes from TCP to DataChannel", n)
		}
	}()

	// DataChannelからTCP接続にデータを送信
	registry.AddHandler(func(msg webrtc.DataChannelMessage) {
		_, err := conn.Write(msg.Data)
		if err != nil {
			log.Printf("Error writing to TCP connection: %v", err)
		} else {
			log.Printf("Forwarded %d bytes from DataChannel to TCP connection", len(msg.Data))
		}
	})
}

func startRemote(remotePort int, registry *MessageHandlerRegistry, dataChannel *webrtc.DataChannel) {
	serverAddress := fmt.Sprintf("localhost:%d", remotePort)
	var conn net.Conn
	var connOnce sync.Once

	// DataChannelからTCP接続にデータを送信
	registry.AddHandler(func(msg webrtc.DataChannelMessage) {
		// 初回メッセージでTCP接続を確立
		connOnce.Do(func() {
			var err error
			conn, err = net.Dial("tcp", serverAddress)
			if err != nil {
				log.Fatalf("Failed to connect to remote server %s: %v", serverAddress, err)
			}
			log.Printf("Connected to remote server %s", serverAddress)
		})

		if conn == nil {
			log.Println("TCP connection is not available")
			return
		}

		// メッセージをTCPサーバーに送信
		_, err := conn.Write(msg.Data)
		if err != nil {
			log.Printf("Error writing to TCP server: %v", err)
		} else {
			log.Printf("Forwarded %d bytes from DataChannel to remote TCP server", len(msg.Data))
		}
	})

	// TCPサーバーからのデータをDataChannelに送信
	go func() {
		buf := make([]byte, 4096)
		for {
			if conn == nil {
				continue
			}

			n, err := conn.Read(buf)
			if err != nil {
				log.Printf("Error reading from TCP server: %v", err)
				return
			}

			// TCPサーバーから受信したデータをDataChannelに送信
			if err := dataChannel.Send(buf[:n]); err != nil {
				log.Printf("Error sending data to DataChannel: %v", err)
			} else {
				log.Printf("Forwarded %d bytes from TCP server to DataChannel", n)
			}
		}
	}()
}

func connectToWebSocket(serverIP string) *websocket.Conn {
	conn, _, err := websocket.DefaultDialer.Dial(serverIP, nil)
	if err != nil {
		log.Fatal("[Error] WebSocket接続: ", err)
	}
	log.Println("WebSocketサーバーに接続")
	return conn
}

func setupWebRTC() (*webrtc.PeerConnection, *webrtc.DataChannel) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		log.Fatal("[Error] PeerConnection作成: ", err)
	}
	log.Println("PeerConnectionを作成")

	dataChannel, err := peerConnection.CreateDataChannel("chat", nil)
	if err != nil {
		log.Fatal("[Error] DataChannel作成: ", err)
	}
	log.Println("DataChannelを作成")

	return peerConnection, dataChannel
}

func setupDataChannelEventHandlers(peerConnection *webrtc.PeerConnection, registry *MessageHandlerRegistry) {
	peerConnection.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		dataChannel.OnOpen(func() {
			log.Println("DataChannel opened")
		})

		dataChannel.OnClose(func() {
			log.Println("DataChannel closed")
			registry.mu.Lock()
			defer registry.mu.Unlock()
			registry.handlers = nil // ハンドラーをクリアしてリソース解放
		})

		dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
			// メッセージを処理
			if msg.IsString {
				fmt.Printf("%s", string(msg.Data))
			} else {
				os.Stdout.Write(msg.Data)
			}
			registry.HandleMessage(msg) // 登録されたすべてのハンドラーを実行
		})
	})
}

func setupPeerConnectionEventHandlers(peerConnection *webrtc.PeerConnection, conn *websocket.Conn, targetID *string, pendingCandidates *[]*webrtc.ICECandidate, clientID string) {
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}

		log.Println("ICE candidate")
		if peerConnection.LocalDescription == nil {
			log.Println("ICE candidate 追加")
			*pendingCandidates = append(*pendingCandidates, candidate)
			return
		}

		sendICECandidate(conn, candidate, *targetID, clientID)
	})

	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Peer connection state changed: %s\n", state.String())
		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			log.Println("Peer connection closed")
			conn.Close()
			os.Exit(0)
		}
	})
}

func sendSignalingRequest(conn *websocket.Conn, clientID string) {
	signalingRequest := SignalingMessage{
		Type:     "signaling_request",
		TargetID: "",
		ID:       clientID,
	}
	err := conn.WriteJSON(signalingRequest)
	if err != nil {
		log.Fatal("[Error] シグナリング要求送信: ", err)
	}
	log.Println("シグナリング要求を送信")
}

func handleSignalingMessages(conn *websocket.Conn, peerConnection *webrtc.PeerConnection, dataChannel *webrtc.DataChannel, targetID *string, pendingCandidates *[]*webrtc.ICECandidate, clientID string) {
	for {
		var message SignalingMessage
		err := conn.ReadJSON(&message)
		if err != nil {
			log.Fatal("[Error] シグナリングメッセージ受信: ", err)
		}
		log.Println("シグナリングメッセージを受信: ", message.Type)

		switch message.Type {
		case "signaling_response":
			if message.Request == "offer" {
				*targetID = message.TargetID
				sendOffer(conn, peerConnection, message.TargetID, clientID)
				sendPendingICECandidates(conn, pendingCandidates, *targetID, clientID)
				*pendingCandidates = []*webrtc.ICECandidate{}
			}
		case "offer":
			*targetID = message.ID
			handleOffer(peerConnection, message.Offer)
			sendAnswer(conn, peerConnection, *targetID, clientID)
			sendPendingICECandidates(conn, pendingCandidates, *targetID, clientID)
			*pendingCandidates = []*webrtc.ICECandidate{}
		case "answer":
			*targetID = message.ID
			handleAnswer(peerConnection, message.Answer)
		case "candidate":
			handleICECandidate(peerConnection, message.Candidate)
		}
	}
}

func sendOffer(conn *websocket.Conn, peerConnection *webrtc.PeerConnection, targetID string, clientID string) {
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		log.Fatal("[Error] Offer作成: ", err)
	}
	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		log.Fatal("[Error] LocalDescription設定: ", err)
	}
	log.Println("Offerを作成")

	offerMessage := OfferMessage{
		Type:     "offer",
		TargetID: targetID,
		Offer:    offer.SDP,
		ID:       clientID,
	}
	err = conn.WriteJSON(offerMessage)
	if err != nil {
		log.Fatal("[Error] Offer送信: ", err)
	}
	log.Println("Offerを送信")
}

func handleOffer(peerConnection *webrtc.PeerConnection, offerSDP string) {
	err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	})
	if err != nil {
		log.Fatal("[Error] RemoteDescription設定: ", err)
	}
	log.Println("Offerを設定")
}

func sendAnswer(conn *websocket.Conn, peerConnection *webrtc.PeerConnection, targetID string, clientID string) {
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Fatal("[Error] Answer作成: ", err)
	}
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		log.Fatal("[Error] LocalDescription設定: ", err)
	}
	log.Println("Answerを作成")

	answerMessage := AnswerMessage{
		Type:     "answer",
		TargetID: targetID,
		Answer:   answer.SDP,
		ID:       clientID,
	}
	err = conn.WriteJSON(answerMessage)
	if err != nil {
		log.Fatal("[Error] Answer送信: ", err)
	}
	log.Println("Answerを送信")
}

func handleAnswer(peerConnection *webrtc.PeerConnection, answerSDP string) {
	err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	})
	if err != nil {
		log.Fatal("RemoteDescription設定エラー: ", err)
	}
	log.Println("Answerを設定")
}

func sendICECandidate(conn *websocket.Conn, candidate *webrtc.ICECandidate, targetID string, clientID string) {
	candidateMessage := CandidateMessage{
		Type:      "candidate",
		TargetID:  targetID,
		Candidate: candidate.ToJSON().Candidate,
		ID:        clientID,
	}
	err := conn.WriteJSON(candidateMessage)
	if err != nil {
		log.Fatal("[Error] ICE candidate送信: ", err)
	}
	log.Println("ICE candidateを送信")
}

func sendPendingICECandidates(conn *websocket.Conn, pendingCandidates *[]*webrtc.ICECandidate, targetID string, clientID string) {
	for _, candidate := range *pendingCandidates {
		sendICECandidate(conn, candidate, targetID, clientID)
	}
}

func handleICECandidate(peerConnection *webrtc.PeerConnection, candidateJSON string) {
	candidate := webrtc.ICECandidateInit{
		Candidate: candidateJSON,
	}
	err := peerConnection.AddICECandidate(candidate)
	if err != nil {
		log.Fatal("[Error] ICE candidate追加エラー: ", err)
	}
	log.Println("ICE candidateを追加")
}

func sendUserMessages(dataChannel *webrtc.DataChannel) {
	reader := bufio.NewReader(os.Stdin)
	for {
		data, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				log.Println("Reached end of stdin")
				return
			}
			log.Fatal("stdin read error: ", err)
		}

		if isBinaryData(data) {
			err = dataChannel.Send(data)
		} else {
			err = dataChannel.SendText(string(data))
		}

		if err != nil {
			log.Fatal("[Error] メッセージ送信: ", err)
		}
	}
}

func isBinaryData(data []byte) bool {
	return !utf8.Valid(data)
}
