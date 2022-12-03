#!/usr/bin/env python

import sys
import collections
import tempfile
import re
import shutil
from subprocess import Popen, PIPE


class Error(Exception):
    pass


def intersected(a, b):
    """
    Returns true if a and b intersect.
    """
    ax1, ay1, ax2, ay2 = a["x"], a["y"], a["x"] + a["w"], a["y"] + a["h"]
    bx1, by1, bx2, by2 = b["x"], b["y"], b["x"] + b["w"], b["y"] + b["h"]
    if ax1 > bx2 or ax2 < bx1 or ay1 > by2 or ay2 < by1:
        return False
    return True


def main():

    # Check requirements

    qpdf_path = shutil.which("qpdf")
    fix_qdf_path = shutil.which("fix-qdf")
    if not qpdf_path or not fix_qdf_path:
        raise Error("qpdf is missing - please install it before retrying")
    inkscape_path = shutil.which("inkscape")
    if not inkscape_path:
        raise Error("inkscape is missing - please install it before retrying")
    if len(sys.argv) < 3:
        raise Error(
            "Usage: %s input.svg output.pdf [inkscape cli flags ...]" % sys.argv[0]
        )
    input_svg_path, output_pdf_path, *inkscape_flags = sys.argv[1:]

    # Load SVG

    with open(input_svg_path) as f:
        svg_content = f.read()

    # Get inkscape document scaling

    # Based on https://oreillymedia.github.io/Using_SVG/guide/units.html
    unit_2_px = {
        "px": 1,
        "pt": 1.33333333333333,
        "mm": 3.7795,
        "pc": 16,
        "cm": 37.795,
        "in": 96,
    }

    svg_tag = re.search(r"<svg[^>]*>", svg_content).group(0)
    svg_width, svg_width_unit = re.search(
        r'\bwidth\s*=\s*"([0-9.]+)(px|pt|mm|pc|cm|in)?"', svg_tag
    ).groups()
    if not svg_width_unit:
        svg_width_unit = "px"
    svg_height, svg_height_unit = re.search(
        r'\bheight\s*=\s*"([0-9.]+)(px|pt|mm|pc|cm|in)?"', svg_tag
    ).groups()
    if not svg_height_unit:
        svg_height_unit = "px"
    svg_viewbox_x, svg_viewbox_y, svg_viewbox_w, svg_viewbox_h = [
        float(v)
        for v in re.search(
            r'\bviewBox\s*=\s*"\s*([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)"',
            svg_tag,
        ).groups()
    ]
    svg_width_pixels = unit_2_px[svg_width_unit] * float(svg_width)
    svg_height_pixels = unit_2_px[svg_height_unit] * float(svg_height)
    x_scale = svg_width_pixels / svg_viewbox_w
    y_scale = svg_height_pixels / svg_viewbox_h

    # Get pages and convert all measurements to pixels.

    pages = {}
    for pi, p in enumerate(re.findall(r"<inkscape:page\s+([^>]*)>", svg_content)):
        x = re.search(r'\bx\s*=\s*"([^"]+)', p).group(1)
        y = re.search(r'\by\s*=\s*"([^"]+)', p).group(1)
        w = re.search(r'\bwidth\s*=\s*"([^"]+)', p).group(1)
        h = re.search(r'\bheight\s*=\s*"([^"]+)', p).group(1)
        pages[pi + 1] = {
            "number": pi + 1,
            "x": float(x) * x_scale,
            "y": float(y) * y_scale,
            "w": float(w) * x_scale,
            "h": float(h) * y_scale,
        }
    if not pages:
        pages[1] = {
            "number": 1,
            "x": svg_viewbox_x * x_scale,
            "y": svg_viewbox_y * y_scale,
            "w": svg_viewbox_w * x_scale,
            "h": svg_viewbox_h * y_scale,
        }

    # Get all objects

    objects = {}
    proc = Popen([inkscape_path, "-S", input_svg_path], stdout=PIPE, stderr=PIPE)
    out, err = [v.decode("utf8") for v in proc.communicate()]
    if "ERROR" in err or ("WARNING" not in err and proc.returncode != 0):
        raise Error("inkscape: " + err)
    for i in re.findall(
        r"^([^,]+),([0-9.]+),([0-9.]+),([0-9.]+),([0-9.]+)$", out, re.MULTILINE
    ):
        objects[i[0]] = {
            "id": i[0],
            "x": float(i[1]),
            "y": float(i[2]),
            "w": float(i[3]),
            "h": float(i[4]),
            "page_locs": [],  # [{x,y,page}]
        }

    # Determine the local positions of objects on all the pages they are visible
    # on.

    for page in pages.values():
        for obj in objects.values():
            if intersected(page, obj):
                obj["page_locs"].append(
                    {
                        "page": page["number"],
                        "x": obj["x"] - page["x"],
                        "y": obj["y"] - page["y"],
                    }
                )

    # Get all links

    anchors = re.findall(r"<a\s([^>]+)>", svg_content)
    links = {}  # id -> href
    for a in anchors:
        id = re.search(r'\bid\s*=\s*"([^"]+)', a)
        href = re.search(r'\bhref\s*=\s*"([^"]+)', a)
        if not href or not id:
            continue
        id = id.group(1)
        href = href.group(1).replace("(", "%28").replace(")", "%29")
        links[id] = href

    # Export SVG as PDF using Inkscape

    im_pdf = tempfile.NamedTemporaryFile(
        mode="rb", prefix="svglinkify-im-pdf-", suffix=".pdf", delete=True
    )
    proc = Popen(
        [
            inkscape_path,
            "--export-type=pdf",
            "--export-overwrite",
            "--export-filename",
            im_pdf.name,
            input_svg_path,
            *inkscape_flags,
        ],
        stdout=PIPE,
        stderr=PIPE,
    )
    _, err = [v.decode("utf8") for v in proc.communicate()]
    if "ERROR" in err or ("WARNING" not in err and proc.returncode != 0):
        raise Error("inkscape: " + err)

    # QDFy the PDF so we can start modifying it

    qdf_tmp = tempfile.NamedTemporaryFile(
        mode="r+b", prefix="svglinkify-qdf-", suffix=".pdf", delete=True
    )
    proc = Popen(
        [
            qpdf_path,
            "--qdf",
            "--stream-data=uncompress",
            "--object-streams=disable",
            "--warning-exit-0",
            im_pdf.name,
            qdf_tmp.name,
        ],
        stdout=PIPE,
        stderr=PIPE,
    )
    _, err = proc.communicate()
    if proc.returncode != 0:
        raise Error(err.decode("utf8"))

    # Load the QDFied PDF

    qdf_tmp.seek(0)
    qdf_content = qdf_tmp.read()

    # Remove all existing links added by Inkscape

    deleted_annots_object_ids = set()
    for m in re.finditer(
        rb"\n\n%%.*\n(\d+) (\d+) obj\n<<\n(?:^ .*\n)*>>\nendobj$",
        qdf_content,
        flags=re.MULTILINE,
    ):
        if b"/Type /Annot" in m.group(0) and b"/Subtype /Link" in m.group(0):
            deleted_annots_object_ids.add((int(m.group(1)), int(m.group(2))))

    # Get next object ID we can use.

    next_object_id = int(
        re.search(b"\n\nxref\n\d+ (\d+)$", qdf_content, re.MULTILINE).group(1)
    )

    # Load object IDs of all pages in the PDF document.

    pdf_pages = {}

    for m in re.finditer(
        rb"^%% Page (\d+)\n%%[^\n]*\n(\d+)\s+(\d+)\s+obj\n.*?^endobj$",
        qdf_content,
        flags=(re.MULTILINE | re.DOTALL),
    ):
        pdf_pages[int(m.group(1))] = {
            "obj_id": int(m.group(2)),
            "obj_gen": int(m.group(3)),
        }

    # Add links to the PDF

    px_to_pt = 0.75
    pdf_page_annots = collections.defaultdict(list)  # page -> [annot_obj_id]
    pdf_annot_objects = []
    for link_id, link_href in links.items():
        if link_id not in objects:
            continue

        if link_href.startswith("#"):
            target = objects.get(link_href[1:])
            if not target:
                print("warn: link target not found: %s" % link_href, file=sys.stderr)
                continue
            # As an object can appear on multiple pages, pick the page where the
            # y position of the target is the highest. This should make sense in
            # most cases.
            page_loc = max(target["page_locs"], key=lambda q: q["y"])
            page = pages[page_loc["page"]]
            pdf_page = pdf_pages[page["number"]]
            action = b"/GoTo /D [ %d %d R /XYZ %f %f 0 ]" % (
                pdf_page["obj_id"],
                pdf_page["obj_gen"],
                page_loc["x"] * px_to_pt,
                (page["h"] - page_loc["y"]) * px_to_pt,
            )
        else:
            action = b"/URI /URI (%s)" % link_href.encode("utf8")

        link_object = objects[link_id]
        for locs in link_object["page_locs"]:
            page = pages[locs["page"]]
            pdf_annot_objects.append(
                b"\n%%QDF: ignore_newline\n"
                b"%d 0 obj\n"
                b"<<\n  /Type /Annot /Subtype /Link /Border [ 0 0 0 ]"
                b" /A << /S %s >>"
                b" /Rect [ %f %f %f %f ]\n>>\n"
                b"endobj\n\n"
                % (
                    next_object_id,
                    action,
                    locs["x"] * px_to_pt,
                    (page["h"] - locs["y"]) * px_to_pt,
                    (locs["x"] + link_object["w"]) * px_to_pt,
                    (page["h"] - locs["y"] - link_object["h"]) * px_to_pt,
                )
            )
            pdf_page_annots[locs["page"]].append(next_object_id)
            next_object_id += 1

    def replace_annots_for_page(m):
        page_number = int(m.group(1))
        raw_annots = b""

        def load_existing_annots(m):
            raw_annots = m.group(1)

        m = re.sub(rb"/Annots\s+\[([^\]]+)\]", load_existing_annots, m.group(0))
        for obj_id, gen_id in deleted_annots_object_ids:
            raw_annots = re.sub(rb"\b%d %d R" % (obj_id, gen_id), b"", raw_annots)
        for obj_id in pdf_page_annots.get(page_number, []):
            raw_annots += b"\n%d 0 R\n" % obj_id
        raw_annots = raw_annots.strip()
        if raw_annots:
            return re.sub(
                rb"^<<", rb"<<\n/Annots [ %s ]" % raw_annots, m, flags=re.MULTILINE
            )
        return m

    qdf_content = re.sub(
        rb"^%% Page (\d+)$.*?^endobj$",
        replace_annots_for_page,
        qdf_content,
        flags=re.MULTILINE | re.DOTALL,
    )

    qdf_content = re.sub(
        rb"^xref$",
        rb"%s\nxref" % b"".join(pdf_annot_objects),
        qdf_content,
        flags=re.MULTILINE,
    )

    # UnQDFy the PDF and save it.

    proc = Popen([fix_qdf_path], stdin=PIPE, stdout=PIPE, stderr=PIPE)
    qdf_content, err = proc.communicate(qdf_content)
    if proc.returncode != 0:
        raise Error("cannot fix the qdf output " + err.decode("utf8"))

    qdf_tmp.truncate()
    qdf_tmp.write(qdf_content)
    qdf_tmp.flush()

    proc = Popen(
        [
            qpdf_path,
            "--object-streams=generate",
            "--stream-data=compress",
            "--warning-exit-0",
            qdf_tmp.name,
            output_pdf_path,
        ],
        stdout=PIPE,
        stderr=PIPE,
    )

    out, err = proc.communicate()
    if proc.returncode != 0:
        raise Error(err.decode("utf8"))


if __name__ == "__main__":
    try:
        main()
    except Error as e:
        print("error: %s" % e, file=sys.stderr)
        exit(1)
