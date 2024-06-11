package main

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "os"
    "flag"
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

func main() {
    var serverIP string
    var enableLogging bool
    flag.StringVar(&serverIP, "server", "", "Signaling Server IP address")
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

    setupDataChannelEventHandlers(dataChannel)

    targetID := ""
    pendingCandidates := []*webrtc.ICECandidate{}

    setupPeerConnectionEventHandlers(peerConnection, conn, &targetID, &pendingCandidates, clientID)

    sendSignalingRequest(conn, clientID)

    go handleSignalingMessages(conn, peerConnection, dataChannel, &targetID, &pendingCandidates, clientID)
    go sendUserMessages(dataChannel)

    // Wait for the program to be interrupted or terminated
    select {}
}

func getServerIP() string {
    fmt.Print("Signaling Server IP address (default: ws://localhost:8080): ")
    reader := bufio.NewReader(os.Stdin)
    serverIP, _ := reader.ReadString('\n')
    serverIP = serverIP[:len(serverIP)-1]

    if serverIP == "" {
        serverIP = "ws://localhost:8080"
    }

    return serverIP
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

func connectToWebSocket(serverIP string) *websocket.Conn {
    conn, _, err := websocket.DefaultDialer.Dial(serverIP, nil)
    if err != nil {
        log.Fatal("WebSocket接続エラー: ", err)
    }
    log.Println("WebSocketサーバーに接続しました")
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
        log.Fatal("PeerConnection作成エラー: ", err)
    }
    log.Println("PeerConnectionを作成しました")

    dataChannel, err := peerConnection.CreateDataChannel("chat", nil)
    if err != nil {
        log.Fatal("DataChannel作成エラー: ", err)
    }
    log.Println("DataChannelを作成しました")

    return peerConnection, dataChannel
}

func setupDataChannelEventHandlers(dataChannel *webrtc.DataChannel) {
    dataChannel.OnOpen(func() {
        log.Println("DataChannel opened")
    })
    dataChannel.OnClose(func() {
        log.Println("DataChannel closed")
    })
    dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
        if msg.IsString {
            fmt.Printf("%s", string(msg.Data))
        } else {
            os.Stdout.Write(msg.Data)
        }
    })
}

func setupPeerConnectionEventHandlers(peerConnection *webrtc.PeerConnection, conn *websocket.Conn, targetID *string, pendingCandidates *[]*webrtc.ICECandidate, clientID string) {
    peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
        log.Printf("New DataChannel: %s\n", dc.Label())

        if dc.Label() != "chat" {
            log.Printf("Unknown DataChannel: %s\n", dc.Label())
            return
        }

        dc.OnOpen(func() {
            log.Println("DataChannel opened")
        })

        dc.OnClose(func() {
            log.Println("DataChannel closed")
        })

        dc.OnMessage(func(msg webrtc.DataChannelMessage) {
            if msg.IsString {
                fmt.Printf("%s", string(msg.Data))
            } else {
                os.Stdout.Write(msg.Data)
            }
        })
    })

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
        log.Fatal("シグナリング要求送信エラー: ", err)
    }
    log.Println("シグナリング要求を送信しました")
}

func handleSignalingMessages(conn *websocket.Conn, peerConnection *webrtc.PeerConnection, dataChannel *webrtc.DataChannel, targetID *string, pendingCandidates *[]*webrtc.ICECandidate, clientID string) {
    for {
        var message SignalingMessage
        err := conn.ReadJSON(&message)
        if err != nil {
            log.Fatal("シグナリングメッセージ受信エラー: ", err)
        }
        log.Println("シグナリングメッセージを受信しました: ", message.Type)

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
        log.Fatal("Offer作成エラー: ", err)
    }
    err = peerConnection.SetLocalDescription(offer)
    if err != nil {
        log.Fatal("LocalDescription設定エラー: ", err)
    }
    log.Println("Offerを作成しました")

    offerMessage := OfferMessage{
        Type:     "offer",
        TargetID: targetID,
        Offer:    offer.SDP,
        ID:       clientID,
    }
    err = conn.WriteJSON(offerMessage)
    if err != nil {
        log.Fatal("Offer送信エラー: ", err)
    }
    log.Println("Offerを送信しました")
}

func handleOffer(peerConnection *webrtc.PeerConnection, offerSDP string) {
    err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
        Type: webrtc.SDPTypeOffer,
        SDP:  offerSDP,
    })
    if err != nil {
        log.Fatal("RemoteDescription設定エラー: ", err)
    }
    log.Println("Offerを設定しました")
}

func sendAnswer(conn *websocket.Conn, peerConnection *webrtc.PeerConnection, targetID string, clientID string) {
    answer, err := peerConnection.CreateAnswer(nil)
    if err != nil {
        log.Fatal("Answer作成エラー: ", err)
    }
    err = peerConnection.SetLocalDescription(answer)
    if err != nil {
        log.Fatal("LocalDescription設定エラー: ", err)
    }
    log.Println("Answerを作成しました")

    answerMessage := AnswerMessage{
        Type:     "answer",
        TargetID: targetID,
        Answer:   answer.SDP,
        ID:       clientID,
    }
    err = conn.WriteJSON(answerMessage)
    if err != nil {
        log.Fatal("Answer送信エラー: ", err)
    }
    log.Println("Answerを送信しました")
}

func handleAnswer(peerConnection *webrtc.PeerConnection, answerSDP string) {
    err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
        Type: webrtc.SDPTypeAnswer,
        SDP:  answerSDP,
    })
    if err != nil {
        log.Fatal("RemoteDescription設定エラー: ", err)
    }
    log.Println("Answerを設定しました")
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
        log.Fatal("ICE candidate送信エラー: ", err)
    }
    log.Println("ICE candidateを送信しました")
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
        log.Fatal("ICE candidate追加エラー: ", err)
    }
    log.Println("ICE candidateを追加しました")
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
            log.Fatal("メッセージ送信エラー: ", err)
        }
        log.Println("メッセージを送信しました")
    }
}

func isBinaryData(data []byte) bool {
    return !utf8.Valid(data)
}
