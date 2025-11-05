const API = location.origin;

async function api(path, method = "GET", body) {
  const res = await fetch(API + path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || res.statusText);
  }
  return res.headers.get("content-type")?.includes("application/json")
    ? res.json()
    : res.text();
}

function saveSession(obj) { localStorage.setItem("session", JSON.stringify(obj)); }
function loadSession() { try { return JSON.parse(localStorage.getItem("session")); } catch { return null; } }
function logout() { localStorage.removeItem("session"); location.reload(); }

function el(sel){ return document.querySelector(sel); }
function option(value, text){ const o=document.createElement("option"); o.value=value; o.textContent=text; return o; }