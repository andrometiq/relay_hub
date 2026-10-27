import { io } from 'socket.io-client';

export function subscribeHub(refresh, connect = io) {
  const prefix = window.location.pathname.startsWith('/hub') ? '/hub' : '';
  const socket = connect({ path: `${prefix}/socket.io`, transports: ['websocket'] });
  let running = false, pending = false, closed = false;
  async function invalidate() {
    pending = true;
    if (running || closed) return;
    running = true;
    try {
      while (pending && !closed) {
        pending = false;
        await refresh();
      }
    } finally { running = false; }
  }
  socket.on('connect', invalidate);
  socket.on('hub:invalidate', invalidate);
  return () => {
    closed = true;
    socket.off('connect', invalidate);
    socket.off('hub:invalidate', invalidate);
    socket.disconnect();
  };
}
