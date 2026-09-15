# Vendored web fonts

Latin-subset woff2 files for `@multica/web`. `next/font/local` reads these at
build time so self-host Docker images do not fetch Google Fonts.

| File | Family | Cut |
| --- | --- | --- |
| `inter-latin-wght-normal.woff2` | Inter | variable, latin, normal |
| `inter-latin-wght-italic.woff2` | Inter | variable, latin, italic |
| `source-serif-4-latin-wght-normal.woff2` | Source Serif 4 | variable, latin, normal |
| `source-serif-4-latin-wght-italic.woff2` | Source Serif 4 | variable, latin, italic |
| `instrument-serif-latin-400-normal.woff2` | Instrument Serif | 400, latin, normal |
| `geist-mono-latin-wght-normal.woff2` | Geist Mono | variable, latin, normal |

Sources: Fontsource latin subsets of the same families previously loaded via
`next/font/google`. All four families are licensed under SIL Open Font License
1.1:

- Inter — https://github.com/rsms/inter
- Source Serif 4 — https://github.com/adobe-fonts/source-serif
- Instrument Serif — https://github.com/googlefonts/instrument-serif
- Geist Mono — https://github.com/vercel/geist-font
