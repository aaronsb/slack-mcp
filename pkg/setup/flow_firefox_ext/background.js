// Background script: receives xoxc from content script, reads HttpOnly d cookie
// via browser.cookies API, POSTs both to localhost callback server.

const CALLBACK_PORT = "{{CALLBACK_PORT}}";

browser.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message.type !== "xoxc_extracted" || !message.xoxc) return;

  browser.cookies.getAll({ domain: ".slack.com", name: "d" }).then((cookies) => {
    const dCookie = cookies.find((c) => c.name === "d");
    if (!dCookie || !dCookie.value) {
      console.error("[slack-mcp] No d cookie found for slack.com");
      return;
    }

    fetch(`http://localhost:${CALLBACK_PORT}/callback`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ xoxc: message.xoxc, xoxd: dCookie.value }),
    })
      .then((r) => r.json())
      .then((data) => {
        if (data.ok) {
          console.log("[slack-mcp] Tokens sent successfully");
        } else {
          console.error("[slack-mcp] Server error:", data.error);
        }
      })
      .catch((err) => console.error("[slack-mcp] Failed to send tokens:", err));
  });
});
