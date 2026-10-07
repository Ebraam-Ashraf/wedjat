
/**
 * AsciiArt — display a piece of ASCII art with the theme's --ascii color.
 * Used for empty states (mask), success states (ankh), and decorative art (eye, pyramid-scene).
 */

export interface AsciiArtProps {
  art: string;
  label?: string;
  className?: string;
}

export default function AsciiArt({ art, label, className = '' }: AsciiArtProps) {
  if (!art) return null;

  return (
    <figure className={`ascii-figure ${className}`}>
      <pre className="ascii-pre" aria-hidden="true">{art}</pre>
      {label && <figcaption className="ascii-label">{label}</figcaption>}
    </figure>
  );
}
