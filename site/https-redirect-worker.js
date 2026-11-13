// Sends every plain-HTTP request to the same URL over HTTPS (301), then serves
// the static assets unchanged. Done here rather than with the zone-wide
// "Always Use HTTPS" setting so only this hostname is affected.
export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.protocol === "http:") {
      url.protocol = "https:";
      return Response.redirect(url.toString(), 301);
    }
    return env.ASSETS.fetch(request);
  },
};
