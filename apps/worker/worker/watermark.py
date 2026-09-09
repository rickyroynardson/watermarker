"""Pure image processing: bytes in, PNG bytes out."""

import warnings
from io import BytesIO

from PIL import Image, ImageCms, ImageOps, UnidentifiedImageError

MAX_BYTES = 10 * 1024 * 1024
MAX_PIXELS = 20_000_000


class InvalidImage(ValueError):
    """Permanent input failure that can be reported to the results queue."""


def decode(data: bytes) -> Image.Image:
    if not 0 < len(data) <= MAX_BYTES:
        raise InvalidImage("image must be between 1 byte and 10 MiB")
    try:
        with warnings.catch_warnings():
            warnings.simplefilter("error", Image.DecompressionBombWarning)
            # Pillow identifies the format from the bytes, not the object Content-Type.
            with Image.open(BytesIO(data), formats=("JPEG", "PNG", "WEBP")) as image:
                if image.width * image.height > MAX_PIXELS:
                    raise InvalidImage("image exceeds 20 million pixels")
                if getattr(image, "n_frames", 1) != 1:
                    raise InvalidImage("animated images are not supported")
                image.load()
                oriented = ImageOps.exif_transpose(image)
                profile = image.info.get("icc_profile")
                if profile:
                    rgba = ImageCms.profileToProfile(
                        oriented, ImageCms.ImageCmsProfile(BytesIO(profile)),
                        ImageCms.createProfile("sRGB"), outputMode="RGBA",
                    )
                else:
                    rgba = oriented.convert("RGBA")
                rgba.info.clear()  # Do not copy source EXIF or an obsolete color profile.
                return rgba
    except InvalidImage:
        raise
    except (UnidentifiedImageError, OSError, ValueError, SyntaxError,
            Image.DecompressionBombWarning, Image.DecompressionBombError,
            ImageCms.PyCMSError) as exc:
        raise InvalidImage("invalid JPEG, PNG, WebP, or color profile") from exc


def composite(source: bytes, watermark: bytes) -> bytes:
    with decode(source) as base, decode(watermark) as mark:
        # Fixed phase-1 styling: fit within 20% of each dimension, never upscale.
        mark.thumbnail((max(1, base.width // 5), max(1, base.height // 5)), Image.Resampling.LANCZOS)
        mark.putalpha(mark.getchannel("A").point(lambda alpha: round(alpha * 0.6)))
        margin = min(base.width, base.height) // 50
        base.alpha_composite(mark, (base.width - mark.width - margin, base.height - mark.height - margin))
        output = BytesIO()
        base.save(output, format="PNG", icc_profile=ImageCms.ImageCmsProfile(ImageCms.createProfile("sRGB")).tobytes())
        return output.getvalue()
