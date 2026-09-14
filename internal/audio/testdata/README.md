# Synthetic audio fixtures

Generated locally with FFmpeg from a 0.5-second 440 Hz sine wave; no external song content.

- `cbr.mp3`: libmp3lame, 44100 Hz stereo, 128 kbit/s, default ID3v2 and Xing tags.
- `raw.mp3`: libmp3lame, 22050 Hz mono, 64 kbit/s, `-id3v2_version 0 -write_xing 0`.
- `vbr.mp3`: libmp3lame, 11025 Hz mono, `-q:a 4`.
- `cover.mp3`: `cbr.mp3` remuxed unchanged with a synthetic 16×16 red PNG as attached artwork.
- `vorbis.ogg`: native `vorbis` encoder, `-strict experimental`, stereo.

Base input: `-f lavfi -i sine=frequency=440:duration=0.5`.
