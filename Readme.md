```
svglinkify converts SVGs to PDFs using inkscape while preserving hyperlinks
applied to objects. To use, first create an SVG file in inkscape. Create
hyperlinks for any object by right clicking the object and selecting "Create
Link". Enter a URL in Href field and save your SVG. Then use svglinkify to
convert your SVG file.

If the hyper link is '#some-id', an internal link is created which when
clicked, will pan and zoom onto the object with id 'some-id'.

Usage: svglinkify input.svg output.pdf [options]

  -inkscape-path string
    	path to inkscape binary (default "/usr/bin/inkscape")
```
