import { useEffect, useRef, useState } from "react";

// A free-text input with a filtered dropdown. items are {value,label?,hint?}.
// The typed value is always accepted (so it works for a URL/path as well as a
// picked item). onPick fires on selection; onInput on every keystroke. Port of
// the typeahead widget in projects-ui.js.
export interface TAItem {
  value: string;
  label?: string;
  hint?: string;
}

export function Typeahead({
  items,
  placeholder,
  className,
  value,
  onChange,
  onPick,
  autoFocus,
}: {
  items: TAItem[];
  placeholder?: string;
  className?: string;
  value: string;
  onChange: (v: string) => void;
  onPick?: (v: string) => void;
  autoFocus?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (autoFocus) inputRef.current?.focus();
  }, [autoFocus]);

  const q = value.trim().toLowerCase();
  const matches = items
    .filter((it) => !q || it.value.toLowerCase().includes(q) || (it.label || "").toLowerCase().includes(q))
    .slice(0, 30);

  const choose = (it: TAItem) => {
    onChange(it.value);
    setOpen(false);
    setActive(-1);
    onPick?.(it.value);
  };

  return (
    <div className="ta">
      <input
        ref={inputRef}
        type="text"
        className={className || "ta-input"}
        placeholder={placeholder}
        autoComplete="off"
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          setActive(-1);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => setTimeout(() => setOpen(false), 120)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setActive((a) => Math.min(a + 1, matches.length - 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setActive((a) => Math.max(a - 1, 0));
          } else if (e.key === "Enter" && active >= 0 && matches[active]) {
            e.preventDefault();
            choose(matches[active]);
          } else if (e.key === "Escape") {
            setOpen(false);
          }
        }}
      />
      {open && matches.length > 0 && (
        <div className="ta-menu">
          {matches.map((it, i) => (
            <div
              key={it.value}
              className={`ta-opt${i === active ? " active" : ""}`}
              onMouseDown={(e) => {
                e.preventDefault();
                choose(it);
              }}
            >
              <span className="ta-val">{it.label || it.value}</span>
              {it.hint && <span className="ta-hint">{it.hint}</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
