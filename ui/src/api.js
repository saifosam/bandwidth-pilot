// Talks to the Go backend. Inside the Windows app the backend is bound in as
// window.pilotCall; in a browser (--serve) it is a localhost endpoint.

// In the app window the page is loaded from a string, so there is no server
// to fetch from. The bridge is injected at document creation; wait briefly
// for it in case the first call races ahead.
async function bridge() {
  if (typeof window.pilotCall === 'function') return window.pilotCall;
  if (location.protocol === 'http:' || location.protocol === 'https:') return null;
  for (let i = 0; i < 60; i++) {
    await new Promise((r) => setTimeout(r, 50));
    if (typeof window.pilotCall === 'function') return window.pilotCall;
  }
  throw new Error('The app window did not provide its backend bridge.');
}

export async function call(method, params) {
  const native = await bridge();
  if (native) return native(method, params ?? null);
  const res = await fetch('/rpc', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Pilot-Token': window.__PILOT_TOKEN || '' },
    body: JSON.stringify({ method, params: params ?? null }),
  });
  if (!res.ok) throw new Error('Backend refused the request (' + res.status + ')');
  const body = await res.json();
  if (!body.ok) throw new Error(body.error);
  return body.result;
}
