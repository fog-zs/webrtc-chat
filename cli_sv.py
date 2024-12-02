import socket
import threading
import sys

# サーバーの処理
def start_server(host='0.0.0.0', port=12345):
    server_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server_socket.bind((host, port))
    server_socket.listen(1)
    print(f"Server started on {host}:{port}. Waiting for connections...")

    client_socket, addr = server_socket.accept()
    print(f"Connection from {addr}")

    stop_event = threading.Event()

    # メッセージの送信
    def send_messages():
        while not stop_event.is_set():
            try:
                message = input("Server: ")
                if stop_event.is_set(): break
                client_socket.sendall(message.encode('utf-8'))
            except Exception:
                stop_event.set()
                break

    # メッセージの受信
    def receive_messages():
        while not stop_event.is_set():
            try:
                data = client_socket.recv(1024).decode('utf-8')
                if not data:
                    print("Client disconnected.")
                    stop_event.set()
                    break
                print(f"Client: {data}")
            except ConnectionResetError:
                print("Client disconnected.")
                stop_event.set()
                break

    # スレッドで送信・受信を並行実行
    sender_thread = threading.Thread(target=send_messages, daemon=True)
    receiver_thread = threading.Thread(target=receive_messages, daemon=True)

    sender_thread.start()
    receiver_thread.start()

    try:
        sender_thread.join()
        receiver_thread.join()
    except KeyboardInterrupt:
        print("\nServer shutting down...")
        stop_event.set()

    client_socket.close()
    server_socket.close()

# クライアントの処理
def start_client(host='127.0.0.1', port=12345):
    client_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    client_socket.connect((host, port))
    print(f"Connected to server {host}:{port}")

    stop_event = threading.Event()

    # メッセージの送信
    def send_messages():
        while not stop_event.is_set():
            try:
                message = input("Client: ")
                if stop_event.is_set(): break
                client_socket.sendall(message.encode('utf-8'))
            except Exception:
                stop_event.set()
                break

    # メッセージの受信
    def receive_messages():
        while not stop_event.is_set():
            try:
                data = client_socket.recv(1024).decode('utf-8')
                if not data:
                    print("Server disconnected.")
                    stop_event.set()
                    break
                print(f"Server: {data}")
            except ConnectionResetError:
                print("Server disconnected.")
                stop_event.set()
                break

    # スレッドで送信・受信を並行実行
    sender_thread = threading.Thread(target=send_messages, daemon=True)
    receiver_thread = threading.Thread(target=receive_messages, daemon=True)

    sender_thread.start()
    receiver_thread.start()

    try:
        sender_thread.join()
        receiver_thread.join()
    except KeyboardInterrupt:
        print("\nClient shutting down...")
        stop_event.set()

    client_socket.close()

# 実行方法
if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Bidirectional TCP Client/Server with graceful shutdown")
    parser.add_argument('mode', choices=['server', 'client'], help="Mode to run the script in: 'server' or 'client'")
    parser.add_argument('--host', default='127.0.0.1', help="Host to connect to or bind (default: 127.0.0.1)")
    parser.add_argument('--port', type=int, default=12345, help="Port to connect to or bind (default: 12345)")

    args = parser.parse_args()

    if args.mode == 'server':
        start_server(args.host, args.port)
    elif args.mode == 'client':
        start_client(args.host, args.port)
