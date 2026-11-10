# Image generation

Valinor can generate images through OpenRouter's Image API. The feature is
configured independently of the chat provider, so chat can stay on DeepSeek (or
any other provider) while images use OpenRouter.

## Setup

1. Create an API key at [openrouter.ai/keys](https://openrouter.ai/keys). Image
   generation is billed per image, so the account needs credits.
2. Add the section to `~/.valinor/config.toml`:

```toml
[image_generation]
provider = "openrouter"      # "" disables the generate_image tool
api_key = "sk-or-v1-..."
model = "bytedance-seed/seedream-5-0-flash"
# base_url = "https://openrouter.ai/api/v1"   # optional; default shown
```

3. Restart Valinor. Configuration is read at startup.

The tool is registered only when `[image_generation]` is configured. Chat
provider settings are not involved.

## Models

The default is ByteDance Seedream 5.0 Flash
(`bytedance-seed/seedream-5-0-flash`), which accepts `1K` and `2K` resolutions
and costs $0.018 per output image. Other OpenRouter image models work with the
same configuration by changing `model`; supported parameters vary by model.

## Tool parameters

`generate_image` accepts:

| Parameter | Description |
| --- | --- |
| `prompt` | Text description of the image, or of the change to apply when references are supplied (required). |
| `images` | Optional workspace-relative paths of reference images to edit, restyle, or combine (at most 14). They are sent to OpenRouter as `input_references` data URIs. |
| `send` | Deliver the image to the current client (default `true`). `false` saves to the workspace only. |
| `aspect_ratio` | Optional ratio, e.g. `1:1`, `16:9`, `9:16`. |
| `size` | Optional resolution tier (`512`, `768`, `1K`, `1.5K`, `2K`, `4K`) or explicit pixels (`1024x1024`). Tiers are sent to OpenRouter as `resolution`; explicit pixels are sent as `size` and take precedence over `aspect_ratio`. |

Images are saved under `<valinor_dir>/workspace/images/` as
`generated-<unix>.<ext>`. With `send=true` they are also delivered inline to
Telegram (when it is enabled) and are available for `send_image` or `read_image`
afterwards.

## Editing existing images

Pass one or more workspace paths in `images` and describe the change in `prompt`:

```
generate_image(images=["uploads/photo.jpg"], prompt="give her red hair, keep the face unchanged")
```

The agent is instructed to call `read_image` first so it can see the photo, then
`generate_image` with the same path. Attachments sent through Telegram are
already saved under `uploads/`, so their paths can be reused directly.

Reference-image support depends on the model. Seedream 5.0 Flash accepts up to
14 references and handles single- or multi-image edits; models without editing
support return an OpenRouter 400 error, which is shown in the tool result.

## Limits

- Generated images larger than 10 MB are rejected (Telegram's photo limit).
  Retry with a smaller resolution such as `1K`.
- Each request generates one image and times out after 120 seconds.
- Reference images are limited to 14 files and 30 MB combined; each file uses
  the same png/jpeg/gif/webp validation as `read_image`.
- OpenRouter returns `b64_json`, so no temporary download URLs are involved.

## Troubleshooting

- **`generate_image` is missing:** `[image_generation]` is incomplete or Valinor
  was not restarted. Check `provider`, `api_key`, and `model`.
- **HTTP 401:** the API key is invalid or lacks access to the model.
- **HTTP 402:** the OpenRouter account has insufficient credits.
- **HTTP 400:** the model rejected `size` or `aspect_ratio` (for example, a 4K
  tier on Seedream 5.0 Flash). Use a supported tier or explicit pixels; the
  OpenRouter error message is returned in the tool result.
- **HTTP 400 with `images`:** the configured model does not support
  `input_references` or the reference count/format. Use an editing-capable model
  such as Seedream 5.0 Flash, or omit `images`.
- **Image too large:** some models return PNG at 2K, which can exceed the 10 MB
  limit. Use a lower tier or a model that returns JPEG (Seedream 5.0 Flash
  returns JPEG).
