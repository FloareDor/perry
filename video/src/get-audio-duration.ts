import { ALL_FORMATS, Input, UrlSource } from "mediabunny";

export async function getAudioDuration(src: string): Promise<number> {
  const input = new Input({
    formats: ALL_FORMATS,
    source: new UrlSource(src, { getRetryDelay: () => null }),
  });
  return input.computeDuration();
}
