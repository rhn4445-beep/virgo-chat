const WebSocket = global.WebSocket;

async function runVerification() {
  console.log("=== VIRGO v2.5.0 REAL-TIME VERIFICATION ===");

  // Check version endpoint
  try {
    const res = await fetch("https://virgo-chat.onrender.com/api/version");
    const v = await res.json();
    console.log("Current Live Version:", v.version);
    if (v.version !== "v2.5.0") {
      console.log("⏳ Deploy still in progress... Waiting 15s...");
      await new Promise(r => setTimeout(r, 15000));
    }
  } catch(e) {
    console.warn("Version fetch:", e.message);
  }

  console.log("\n--- TEST 1: Canonical Phone Number Matching (+91 vs 10-digit) ---");
  // Client A: App registered with 10-digit number
  const wsApp = new WebSocket("wss://virgo-chat.onrender.com/ws?userId=9537272106");
  // Client B: Browser registered with username
  const wsBrowser = new WebSocket("wss://virgo-chat.onrender.com/ws?userId=browser_user");

  let appMsgs = [];
  let browserMsgs = [];

  wsApp.onmessage = (e) => {
    const d = JSON.parse(e.data);
    console.log("📱 APP (9537272106) received:", d.type, "from", d.from, d.id || "");
    appMsgs.push(d);
  };

  wsBrowser.onmessage = (e) => {
    const d = JSON.parse(e.data);
    console.log("💻 BROWSER received:", d.type, "from", d.from, d.id || "");
    browserMsgs.push(d);
  };

  await Promise.all([
    new Promise(r => wsApp.onopen = r),
    new Promise(r => wsBrowser.onopen = r)
  ]);
  console.log("Both connected!");

  // Test Heartbeat
  console.log("\n--- TEST 2: WebSocket Heartbeat ---");
  wsApp.send(JSON.stringify({ type: "heartbeat", from: "9537272106" }));
  await new Promise(r => setTimeout(r, 1000));

  // Test Browser -> App with +91 prefix
  console.log("\n--- TEST 3: Browser sends to +919537272106 ---");
  wsBrowser.send(JSON.stringify({
    id: "msg-b2a-1",
    from: "browser_user",
    to: "+919537272106",
    type: "chat",
    payload: { text: "Hello App from Browser!" },
    timestamp: Date.now()
  }));

  await new Promise(r => setTimeout(r, 2000));

  // Test App -> Browser
  console.log("\n--- TEST 4: App sends to Browser ---");
  wsApp.send(JSON.stringify({
    id: "msg-a2b-1",
    from: "9537272106",
    to: "browser_user",
    type: "chat",
    payload: { text: "Hello Browser from App!" },
    timestamp: Date.now()
  }));

  await new Promise(r => setTimeout(r, 2000));

  wsApp.close();
  wsBrowser.close();

  const appGotMsg = appMsgs.some(m => m.id === "msg-b2a-1" && m.type === "chat");
  const browserGotMsg = browserMsgs.some(m => m.id === "msg-a2b-1" && m.type === "chat");
  const heartbeatOk = appMsgs.some(m => m.type === "heartbeat_ack");

  console.log("\n=== FINAL RESULTS ===");
  console.log("1. App received message sent to +919537272106:", appGotMsg ? "✅ PASS" : "❌ FAIL");
  console.log("2. Browser received message from App:", browserGotMsg ? "✅ PASS" : "❌ FAIL");
  console.log("3. Heartbeat ACK received:", heartbeatOk ? "✅ PASS" : "❌ FAIL");
}

runVerification();
