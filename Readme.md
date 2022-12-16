Export Inkscape SVGs to PDF while preserving hyperlinks (including
internally linked fragment links).

Install it from AUR: `yay -S svglinkify-git`

This script depends on recent versions of:

 - [qpdf](https://qpdf.sourceforge.io/) (only tested with version `11.1.1`).
 - [Inkscape](https://inkscape.org/) (only tested with version `1.2.1`).

Once you've created an SVG with hyperlinks, run:

```
./svglinkify.py input.svg output.pdf
```

See [the demo video](./demo.webm).
