// Content script: extracts xoxc token from localStorage on app.slack.com,
// sends it to the background script which handles the HttpOnly d cookie.

(function () {
  let xoxc = null;

  for (const [k, v] of Object.entries(localStorage)) {
    try {
      const search = (o) => {
        if (typeof o === "string" && o.startsWith("xoxc-")) return o;
        if (typeof o === "object" && o) {
          for (const val of Object.values(o)) {
            const r = search(val);
            if (r) return r;
          }
        }
      };
      const parsed =
        typeof v === "string" && v.startsWith("{") ? JSON.parse(v) : v;
      const r = search(parsed);
      if (r) {
        xoxc = r;
        break;
      }
    } catch (e) {}
    if (typeof v === "string" && v.startsWith("xoxc-")) {
      xoxc = v;
      break;
    }
  }

  if (xoxc) {
    browser.runtime.sendMessage({ type: "xoxc_extracted", xoxc: xoxc });
  } else {
    console.error(
      "[slack-mcp] No xoxc token found in localStorage — are you logged into Slack?"
    );
  }
})();
