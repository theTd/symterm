import type { AdminEvent } from './api';

type WSMessage =
  | { type: 'hello'; cursor: number }
  | { type: 'heartbeat'; cursor: number }
  | { type: 'cursor_expired' }
  | { type: 'auth_error'; message: string }
  | { type: 'event'; event: AdminEvent };

export function createAdminWebSocket(
  cursor: number,
  handlers: {
    onConnecting?: (cursor: number) => void;
    onOpen?: (cursor: number) => void;
    onEvent?: (event: AdminEvent) => void;
    onCursorExpired?: () => void;
    onAuthError?: (message: string) => void;
    onClose?: (cursor: number) => void;
  },
) {
  let closed = false;
  let latestCursor = cursor;
  let socket: WebSocket | null = null;
  let timer: number | undefined;

  const connect = () => {
    handlers.onConnecting?.(latestCursor);
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    socket = new WebSocket(`${protocol}//${window.location.host}/admin/ws?cursor=${latestCursor}`);
    socket.onmessage = (message) => {
      const payload = JSON.parse(message.data) as WSMessage;
      switch (payload.type) {
        case 'hello':
          latestCursor = payload.cursor || latestCursor;
          handlers.onOpen?.(latestCursor);
          break;
        case 'event':
          latestCursor = payload.event.cursor || latestCursor;
          handlers.onEvent?.(payload.event);
          break;
        case 'cursor_expired':
          handlers.onCursorExpired?.();
          break;
        case 'auth_error':
          handlers.onAuthError?.(payload.message);
          break;
        case 'heartbeat':
          latestCursor = payload.cursor || latestCursor;
          break;
      }
    };
    socket.onclose = () => {
      if (!closed) {
        handlers.onClose?.(latestCursor);
        timer = window.setTimeout(connect, 1000);
      }
    };
  };

  connect();

  return {
    close() {
      closed = true;
      if (timer !== undefined) {
        window.clearTimeout(timer);
      }
      socket?.close();
    },
    setCursor(next: number) {
      latestCursor = next;
    },
  };
}
