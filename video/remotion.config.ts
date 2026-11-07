import { Config } from "@remotion/cli/config";

Config.setVideoImageFormat("jpeg");
Config.setOverwriteOutput(true);
// The scenes are flat colour and large type, so a high CRF would show
// banding on the gradients before it saved anything worth having.
Config.setCrf(18);

// The headless shell download does not complete on every network, so the
// render uses whatever Chrome is installed. Override with --browser-executable.
if (process.platform === "win32") {
  Config.setBrowserExecutable("C:/Program Files/Google/Chrome/Application/chrome.exe");
}
