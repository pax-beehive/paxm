#!/usr/bin/env ruby

require "fileutils"

ROOT = File.expand_path("..", __dir__)
ASSETS = File.join(ROOT, "docs", "assets")
WORK = File.join(ROOT, ".tmp-readme-gifs")

C = {
  ink: "#05070d",
  panel: "#0b1020",
  line: "#25304a",
  text: "#f5f7fb",
  muted: "#7f8ba7",
  blue: "#6ba8ff",
  violet: "#a783ff",
  mint: "#6ee7c2",
  amber: "#f3c776"
}.freeze

def esc(value)
  value.to_s.gsub("&", "&amp;").gsub("<", "&lt;").gsub(">", "&gt;")
end

def label(x, y, value, size: 20, color: C[:text], weight: 500, anchor: "start", tracking: 0)
  %(<text x="#{x}" y="#{y}" fill="#{color}" font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif" font-size="#{size}" font-weight="#{weight}" text-anchor="#{anchor}" letter-spacing="#{tracking}">#{esc(value)}</text>)
end

def bezier(a, b, c, t)
  u = 1.0 - t
  [
    (u * u * a[0]) + (2 * u * t * b[0]) + (t * t * c[0]),
    (u * u * a[1]) + (2 * u * t * b[1]) + (t * t * c[1])
  ]
end

def polyline(points, t)
  segments = points.length - 1
  scaled = [[t, 0].max, 0.999_999].min * segments
  index = scaled.floor
  local = scaled - index
  a = points[index]
  b = points[index + 1]
  [a[0] + ((b[0] - a[0]) * local), a[1] + ((b[1] - a[1]) * local)]
end

def pulse(x, y, color)
  <<~SVG
    <circle cx="#{x.round(2)}" cy="#{y.round(2)}" r="18" fill="#{color}" opacity="0.08" filter="url(#soft-glow)"/>
    <circle cx="#{x.round(2)}" cy="#{y.round(2)}" r="8" fill="#{color}" opacity="0.24"/>
    <circle cx="#{x.round(2)}" cy="#{y.round(2)}" r="3.8" fill="#ffffff"/>
  SVG
end

def node(x, y, title, subtitle, active: false, align: "start")
  anchor = align == "end" ? "end" : "start"
  text_x = align == "end" ? x - 18 : x + 18
  color = active ? C[:text] : C[:muted]
  dot = active ? C[:blue] : C[:line]
  <<~SVG
    <circle cx="#{x}" cy="#{y}" r="5" fill="#{dot}"/>
    #{label(text_x, y - 3, title, size: 16, color: color, weight: 650, anchor: anchor)}
    #{label(text_x, y + 18, subtitle, size: 12, color: C[:muted], weight: 450, anchor: anchor)}
  SVG
end

def base_svg(kicker, title, subtitle, body)
  <<~SVG
    <svg xmlns="http://www.w3.org/2000/svg" width="1000" height="1000" viewBox="0 0 1000 1000">
      <svg x="0" y="220" width="1000" height="560" viewBox="0 0 1000 560">
        <defs>
          <radialGradient id="ambient" cx="50%" cy="48%" r="62%">
            <stop offset="0" stop-color="#142243"/>
            <stop offset="0.48" stop-color="#0a1020"/>
            <stop offset="1" stop-color="#{C[:ink]}"/>
          </radialGradient>
          <linearGradient id="signal" x1="0" y1="0" x2="1" y2="0">
            <stop offset="0" stop-color="#{C[:blue]}"/>
            <stop offset="1" stop-color="#{C[:violet]}"/>
          </linearGradient>
          <pattern id="grid" width="32" height="32" patternUnits="userSpaceOnUse">
            <path d="M 32 0 L 0 0 0 32" fill="none" stroke="#ffffff" stroke-opacity="0.022" stroke-width="1"/>
          </pattern>
          <filter id="soft-glow" x="-200%" y="-200%" width="500%" height="500%">
            <feGaussianBlur stdDeviation="12"/>
          </filter>
          <filter id="hub-shadow" x="-80%" y="-80%" width="260%" height="260%">
            <feGaussianBlur in="SourceAlpha" stdDeviation="18" result="blur"/>
            <feFlood flood-color="#{C[:blue]}" flood-opacity="0.16"/>
            <feComposite in2="blur" operator="in"/>
            <feMerge><feMergeNode/><feMergeNode in="SourceGraphic"/></feMerge>
          </filter>
        </defs>
        <rect width="1000" height="560" rx="30" fill="url(#ambient)"/>
        <rect width="1000" height="560" rx="30" fill="url(#grid)"/>
        #{label(52, 52, kicker.upcase, size: 11, color: C[:blue], weight: 700, tracking: 2.8)}
        #{label(52, 88, title, size: 30, color: C[:text], weight: 720)}
        #{label(52, 116, subtitle, size: 14, color: C[:muted], weight: 450)}
        #{body}
      </svg>
    </svg>
  SVG
end

def adaptor_hub(active: true)
  ring_color = active ? C[:blue] : C[:line]
  <<~SVG
    <circle cx="500" cy="290" r="92" fill="#0b1222" fill-opacity="0.88" stroke="#{ring_color}" stroke-opacity="0.5" filter="url(#hub-shadow)"/>
    <circle cx="500" cy="290" r="72" fill="none" stroke="#ffffff" stroke-opacity="0.07"/>
    <path d="M 456 266 Q 500 226 544 266 Q 560 312 520 344 Q 472 354 448 312 Q 438 284 456 266" fill="none" stroke="url(#signal)" stroke-width="1.4" stroke-opacity="0.7"/>
    <circle cx="456" cy="266" r="4" fill="#{C[:blue]}"/>
    <circle cx="544" cy="266" r="4" fill="#{C[:violet]}"/>
    <circle cx="520" cy="344" r="4" fill="#{C[:mint]}"/>
    <circle cx="448" cy="312" r="4" fill="#{C[:amber]}"/>
    #{label(500, 291, "PAXM", size: 25, color: C[:text], weight: 760, anchor: "middle", tracking: 1.2)}
    #{label(500, 316, "MEMORY ADAPTOR", size: 9, color: C[:muted], weight: 650, anchor: "middle", tracking: 1.7)}
  SVG
end

def routing_frame(phase)
  source = [220, 290]
  hub_left = [408, 290]
  hub_right = [592, 290]
  provider = [790, 242]
  response = [220, 340]

  source_active = phase < 7 || phase >= 17
  provider_active = phase >= 7 && phase < 18
  if phase < 7
    dot = bezier(source, [318, 252], hub_left, phase / 6.0)
    dot_color = C[:blue]
  elsif phase < 13
    dot = bezier(hub_right, [690, 254], provider, (phase - 7) / 5.0)
    dot_color = C[:violet]
  elsif phase < 18
    dot = bezier(provider, [696, 328], hub_right, (phase - 13) / 4.0)
    dot_color = C[:mint]
  else
    dot = bezier(hub_left, [316, 364], response, (phase - 18) / 5.0)
    dot_color = C[:mint]
  end

  body = +""
  body << %(<path d="M 220 290 Q 318 252 408 290" fill="none" stroke="#{C[:line]}" stroke-width="1.4"/>)
  body << %(<path d="M 592 290 Q 690 254 790 242" fill="none" stroke="#{C[:line]}" stroke-width="1.4"/>)
  body << %(<path d="M 790 242 Q 696 328 592 290" fill="none" stroke="#{C[:line]}" stroke-width="1.1" stroke-dasharray="4 8"/>)
  body << %(<path d="M 408 290 Q 316 364 220 340" fill="none" stroke="#{C[:line]}" stroke-width="1.1" stroke-dasharray="4 8"/>)

  body << node(128, 202, "CODEX", "skill + hooks", active: source_active)
  body << node(128, 258, "CLAUDE CODE", "plugin + MCP", active: source_active)
  body << node(128, 314, "OPENCODE", "global plugin", active: source_active)
  body << node(128, 370, "ANY MCP CLIENT", "stdio tools", active: source_active)

  body << node(872, 186, "SQLITE", "zero-setup default", active: false, align: "end")
  body << node(872, 242, "OPENVIKING", "self-hosted memory", active: provider_active, align: "end")
  body << node(872, 298, "ZEP / MEM0", "managed or private", active: false, align: "end")
  body << node(872, 354, "JSON-RPC", "your provider", active: false, align: "end")

  body << adaptor_hub
  body << pulse(dot[0], dot[1], dot_color)
  body << label(500, 437, "one contract  ·  profiles  ·  routing  ·  reliability", size: 13, color: C[:muted], weight: 520, anchor: "middle", tracking: 0.7)
  body << label(500, 475, phase < 7 ? "request" : phase < 13 ? "route" : phase < 18 ? "recall" : "ranked response", size: 11, color: dot_color, weight: 700, anchor: "middle", tracking: 2.2)

  base_svg("PAXM / ADAPTOR FLOW", "Memory without the lock-in.", "One agent surface. Any memory provider.", body)
end

def stage(x, y, eyebrow, title, active: false, align: "middle")
  color = active ? C[:text] : C[:muted]
  dot = active ? C[:blue] : C[:line]
  <<~SVG
    <circle cx="#{x}" cy="#{y}" r="5" fill="#{dot}"/>
    #{label(x, y - 25, eyebrow.upcase, size: 9, color: active ? C[:blue] : C[:muted], weight: 700, anchor: align, tracking: 1.7)}
    #{label(x, y + 34, title, size: 15, color: color, weight: 620, anchor: align)}
  SVG
end

def passive_frame(phase)
  recall_path = [[112, 238], [340, 238], [610, 238], [870, 238]]
  write_path = [[112, 386], [390, 386], [650, 386], [870, 386]]
  recall_phase = phase < 12
  t = recall_phase ? phase / 11.0 : (phase - 12) / 11.0
  dot = polyline(recall_phase ? recall_path : write_path, t)
  dot_color = recall_phase ? C[:violet] : C[:mint]

  body = +""
  body << %(<line x1="112" y1="238" x2="870" y2="238" stroke="#{C[:line]}" stroke-width="1.3"/>)
  body << %(<line x1="112" y1="386" x2="870" y2="386" stroke="#{C[:line]}" stroke-width="1.3"/>)
  body << %(<path d="M 340 238 C 420 238 420 386 390 386" fill="none" stroke="#{C[:line]}" stroke-width="1" stroke-dasharray="3 7"/>)

  top_index = [[(t * 3).floor, 0].max, 3].min
  bottom_index = top_index
  body << stage(112, 238, "01", "user prompt", active: recall_phase && top_index >= 0)
  body << stage(340, 238, "02", "PAXM hook", active: recall_phase && top_index >= 1)
  body << stage(610, 238, "03", "provider recall", active: recall_phase && top_index >= 2)
  body << stage(870, 238, "04", "model context", active: recall_phase && top_index >= 3)

  body << stage(112, 386, "01", "completed turn", active: !recall_phase && bottom_index >= 0)
  body << stage(390, 386, "02", "durable queue", active: !recall_phase && bottom_index >= 1)
  body << stage(650, 386, "03", "provider delivery", active: !recall_phase && bottom_index >= 2)
  body << stage(870, 386, "04", "retry-safe", active: !recall_phase && bottom_index >= 3)

  body << pulse(dot[0], dot[1], dot_color)
  body << label(52, 184, "BEFORE THE MODEL", size: 10, color: C[:violet], weight: 720, tracking: 2.1)
  body << label(52, 332, "AFTER THE TURN", size: 10, color: C[:mint], weight: 720, tracking: 2.1)
  body << label(500, 482, recall_phase ? "relevant context, only when needed" : "acknowledge locally, deliver in the background", size: 13, color: C[:muted], weight: 520, anchor: "middle", tracking: 0.5)

  base_svg("PAXM / PASSIVE MEMORY", "Memory that moves with the agent.", "Recall before the model. Capture after the turn.", body)
end

def run!(*command)
  abort "command failed: #{command.join(' ')}" unless system(*command)
end

def build_animation(name, frame_builder)
  directory = File.join(WORK, name)
  FileUtils.mkdir_p(directory)
  svg_paths = 24.times.map do |phase|
    path = File.join(directory, format("%02d.svg", phase))
    File.write(path, frame_builder.call(phase))
    path
  end

  run!("qlmanage", "-t", "-s", "1000", "-o", directory, *svg_paths)
  png_paths = svg_paths.map do |svg_path|
    thumbnail = "#{svg_path}.png"
    png = svg_path.sub(/\.svg\z/, ".png")
    run!("sips", "-c", "560", "1000", thumbnail, "--out", png)
    png
  end

  concat = File.join(directory, "frames.txt")
  File.open(concat, "w") do |file|
    png_paths.each do |path|
      file.puts "file '#{path}'"
      file.puts "duration 0.18"
    end
    file.puts "file '#{png_paths.last}'"
    file.puts "duration 0.85"
  end

  output = File.join(ASSETS, "#{name}.gif")
  filter = "fps=12,split[s0][s1];[s0]palettegen=max_colors=96:stats_mode=full[p];[s1][p]paletteuse=dither=bayer:bayer_scale=4"
  run!("ffmpeg", "-y", "-hide_banner", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", concat, "-filter_complex", filter, "-gifflags", "-transdiff", output)
  output
end

FileUtils.rm_rf(WORK)
FileUtils.mkdir_p(WORK)
FileUtils.mkdir_p(ASSETS)

outputs = [
  build_animation("paxm-provider-routing", method(:routing_frame)),
  build_animation("paxm-passive-memory", method(:passive_frame))
]

puts "Generated:"
outputs.each { |path| puts "  #{path}" }
FileUtils.rm_rf(WORK)
