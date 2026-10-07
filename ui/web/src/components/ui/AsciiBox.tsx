import type { ReactNode } from 'react';

/**
 * AsciiBox — character-border panel (ascii/cnlibs inspired)
 *
 * Renders a panel whose border is constructed entirely from Unicode
 * box-drawing characters. Everything lives on a monospace character
 * grid using `ch` / `lh` units so the frame scales with the font.
 *
 * Default glyph set (Unicode box-drawing):
 *   topLeft     ┌  topRight    ┐
 *   bottomLeft  └  bottomRight ┘
 *   horizontal  ─  vertical    │
 *   tee-right   ├  tee-left    ┤
 *   cross       ┼
 *
 * Usage:
 *   <AsciiBox title="GPU STATUS">
 *     <p>content here</p>
 *   </AsciiBox>
 *
 *   // With a subtitle on the right of the title bar
 *   <AsciiBox title="VRAM" subtitle="12.4 GB">…</AsciiBox>
 *
 *   // Double-line variant
 *   <AsciiBox variant="double" title="ALERTS">…</AsciiBox>
 *
 *   // Custom glyph set
 *   <AsciiBox glyphs={{ tl:'╔', tr:'╗', bl:'╚', br:'╝', h:'═', v:'║' }}>…</AsciiBox>
 */

export type AsciiBoxVariant = 'single' | 'double' | 'rounded' | 'heavy' | 'dotted';

export interface AsciiBoxGlyphs {
  tl: string; // top-left corner
  tr: string; // top-right corner
  bl: string; // bottom-left corner
  br: string; // bottom-right corner
  h:  string; // horizontal fill
  v:  string; // vertical fill
}

const GLYPH_SETS: Record<AsciiBoxVariant, AsciiBoxGlyphs> = {
  single:  { tl: '┌', tr: '┐', bl: '└', br: '┘', h: '─', v: '│' },
  double:  { tl: '╔', tr: '╗', bl: '╚', br: '╝', h: '═', v: '║' },
  rounded: { tl: '╭', tr: '╮', bl: '╰', br: '╯', h: '─', v: '│' },
  heavy:   { tl: '┏', tr: '┓', bl: '┗', br: '┛', h: '━', v: '┃' },
  dotted:  { tl: '·', tr: '·', bl: '·', br: '·', h: '·', v: '·' },
};

export interface AsciiBoxProps {
  /** Box-drawing variant. Defaults to 'single'. */
  variant?: AsciiBoxVariant;
  /** Override individual glyph characters. */
  glyphs?: Partial<AsciiBoxGlyphs>;
  /** Optional title embedded in the top border. */
  title?: ReactNode;
  /** Optional subtitle/status embedded in the top border (right side). */
  subtitle?: ReactNode;
  /** Optional footer text embedded in the bottom border. */
  footer?: ReactNode;
  /** Panel content. */
  children?: ReactNode;
  className?: string;
  /** Inner content padding. Defaults to 'normal'. */
  padding?: 'none' | 'tight' | 'normal' | 'loose';
  /** Font size override. The whole box scales from this. */
  fontSize?: string;
  /** ARIA role for the panel (defaults to 'region'). */
  role?: string;
  /** ARIA label. Falls back to title if provided. */
  'aria-label'?: string;
  /** Tone / accent colour class applied to all border chars. */
  tone?: 'default' | 'ok' | 'warn' | 'critical' | 'dim';
}

const PADDING_CLASSES: Record<NonNullable<AsciiBoxProps['padding']>, string> = {
  none:   'px-0 py-0',
  tight:  'px-[1ch] py-[0.5lh]',
  normal: 'px-[2ch] py-[1lh]',
  loose:  'px-[3ch] py-[1.5lh]',
};

const TONE_CLASSES: Record<NonNullable<AsciiBoxProps['tone']>, string> = {
  default:  'text-ascii',
  ok:       'text-ok',
  warn:     'text-warn',
  critical: 'text-critical',
  dim:      'text-text-dim',
};

/**
 * BorderChar — renders a single box-drawing character.
 * Using a separate component so we can swap glyph sets per-instance
 * without repeating markup.
 */
function BC({ ch, className = '' }: { ch: string; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={`select-none leading-none ${className}`}
    >
      {ch}
    </span>
  );
}

/**
 * TitleBar — constructs the top border row.
 *
 *  ┌── TITLE ─────────────────────── SUBTITLE ──┐
 *
 * The "fill" between title and subtitle is `─` chars rendered via a
 * flex-grow spacer so the box naturally fills its container width on
 * the character grid.
 */
function TitleBar({
  g,
  title,
  subtitle,
  toneClass,
}: {
  g: AsciiBoxGlyphs;
  title?: ReactNode;
  subtitle?: ReactNode;
  toneClass: string;
}) {
  const hasTitle    = title    != null && title    !== '';
  const hasSubtitle = subtitle != null && subtitle !== '';

  return (
    <div
      className={`flex items-center w-full font-mono select-none overflow-hidden ${toneClass}`}
      aria-hidden="true"
    >
      {/* Left corner */}
      <BC ch={g.tl} />

      {/* Horizontal fill before title */}
      <BC ch={g.h} className="w-[2ch] shrink-0" />

      {/* Title text (not a border char — keep it white/text so it's readable) */}
      {hasTitle && (
        <span className="px-[1ch] text-text whitespace-nowrap text-xs font-mono font-semibold uppercase tracking-widest">
          {title}
        </span>
      )}

      {/* Expanding fill line */}
      <span
        className="flex-1 overflow-hidden leading-none"
        style={{
          backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='1ch' height='1lh'%3E%3Ctext y='1em' fill='currentColor' font-family='monospace'%3E${encodeURIComponent(g.h)}%3C/text%3E%3C/svg%3E")`,
          backgroundRepeat: 'repeat-x',
          backgroundPosition: 'center',
          height: '1lh',
          minWidth: '2ch',
        }}
      >
        {/* CSS background hack for horizontal fill — no actual chars */}
      </span>

      {/* Subtitle text (right side) */}
      {hasSubtitle && (
        <>
          <span className="px-[1ch] text-text-dim whitespace-nowrap text-xs font-mono">
            {subtitle}
          </span>
          <BC ch={g.h} className="w-[1ch] shrink-0" />
        </>
      )}

      {/* Right corner */}
      <BC ch={g.tr} />
    </div>
  );
}

/**
 * FooterBar — constructs the bottom border row.
 *
 *  └── footer text ──────────────────────────────┘
 */
function FooterBar({
  g,
  footer,
  toneClass,
}: {
  g: AsciiBoxGlyphs;
  footer?: ReactNode;
  toneClass: string;
}) {
  const hasFooter = footer != null && footer !== '';

  return (
    <div
      className={`flex items-center w-full font-mono select-none overflow-hidden ${toneClass}`}
      aria-hidden="true"
    >
      <BC ch={g.bl} />
      <BC ch={g.h} className="w-[2ch] shrink-0" />

      {hasFooter && (
        <span className="px-[1ch] text-text-dim whitespace-nowrap text-xs font-mono">
          {footer}
        </span>
      )}

      <span
        className="flex-1 overflow-hidden leading-none"
        style={{
          backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='1ch' height='1lh'%3E%3Ctext y='1em' fill='currentColor' font-family='monospace'%3E${encodeURIComponent(g.h)}%3C/text%3E%3C/svg%3E")`,
          backgroundRepeat: 'repeat-x',
          backgroundPosition: 'center',
          height: '1lh',
          minWidth: '2ch',
        }}
      >
      </span>

      <BC ch={g.br} />
    </div>
  );
}

/**
 * AsciiBox — main component.
 */
export default function AsciiBox({
  variant = 'single',
  glyphs: glyphOverrides,
  title,
  subtitle,
  footer,
  children,
  className = '',
  padding = 'normal',
  fontSize,
  role = 'region',
  tone = 'default',
  'aria-label': ariaLabel,
}: AsciiBoxProps) {
  const g: AsciiBoxGlyphs = {
    ...GLYPH_SETS[variant],
    ...glyphOverrides,
  };

  const toneClass  = TONE_CLASSES[tone];
  const paddingCls = PADDING_CLASSES[padding];
  const label      = ariaLabel ?? (typeof title === 'string' ? title : undefined);

  return (
    <div
      role={role}
      aria-label={label}
      className={`flex flex-col font-mono ${className}`}
      style={fontSize ? { fontSize } : undefined}
    >
      {/* TOP BORDER */}
      <TitleBar g={g} title={title} subtitle={subtitle} toneClass={toneClass} />

      {/* BODY — left and right vertical borders + inner content */}
      <div className="flex flex-1 min-h-0">
        {/* Left vertical border */}
        <div
          className={`${toneClass} font-mono select-none shrink-0 flex flex-col`}
          aria-hidden="true"
        >
          {/* Rendered as a CSS border so it stretches to full height */}
          <span
            className="w-[1ch] flex-1"
            style={{
              backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='1ch' height='1lh'%3E%3Ctext y='1em' fill='currentColor' font-family='monospace'%3E${encodeURIComponent(g.v)}%3C/text%3E%3C/svg%3E")`,
              backgroundRepeat: 'repeat-y',
              backgroundPosition: 'top center',
              minHeight: '1lh',
            }}
          />
        </div>

        {/* Inner content */}
        <div className={`flex-1 min-w-0 ${paddingCls}`}>
          {children}
        </div>

        {/* Right vertical border */}
        <div
          className={`${toneClass} font-mono select-none shrink-0 flex flex-col`}
          aria-hidden="true"
        >
          <span
            className="w-[1ch] flex-1"
            style={{
              backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='1ch' height='1lh'%3E%3Ctext y='1em' fill='currentColor' font-family='monospace'%3E${encodeURIComponent(g.v)}%3C/text%3E%3C/svg%3E")`,
              backgroundRepeat: 'repeat-y',
              backgroundPosition: 'top center',
              minHeight: '1lh',
            }}
          />
        </div>
      </div>

      {/* BOTTOM BORDER */}
      <FooterBar g={g} footer={footer} toneClass={toneClass} />
    </div>
  );
}

/**
 * AsciiRule — a single horizontal rule using box-drawing chars.
 * Useful as a section divider inside an AsciiBox or standalone.
 *
 *  ├─── label ──────────────────────────────────────────────┤
 */
export function AsciiRule({
  label,
  variant = 'single',
  glyphs: glyphOverrides,
  tone = 'default',
  className = '',
}: {
  label?: ReactNode;
  variant?: AsciiBoxVariant;
  glyphs?: Partial<AsciiBoxGlyphs>;
  tone?: AsciiBoxProps['tone'];
  className?: string;
}) {
  const g = { ...GLYPH_SETS[variant], ...glyphOverrides };
  const toneClass = TONE_CLASSES[tone ?? 'default'];
  const hasLabel = label != null && label !== '';

  return (
    <div
      className={`flex items-center w-full font-mono select-none overflow-hidden ${toneClass} ${className}`}
      aria-hidden="true"
    >
      {/* Left tee or just horizontal */}
      <BC ch="├" />
      <BC ch={g.h} className="w-[2ch] shrink-0" />

      {hasLabel && (
        <span className="px-[1ch] text-text-dim whitespace-nowrap text-xs font-mono">
          {label}
        </span>
      )}

      <span
        className="flex-1 overflow-hidden leading-none"
        style={{
          backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='1ch' height='1lh'%3E%3Ctext y='1em' fill='currentColor' font-family='monospace'%3E${encodeURIComponent(g.h)}%3C/text%3E%3C/svg%3E")`,
          backgroundRepeat: 'repeat-x',
          backgroundPosition: 'center',
          height: '1lh',
          minWidth: '2ch',
        }}
      />

      <BC ch="┤" />
    </div>
  );
}
