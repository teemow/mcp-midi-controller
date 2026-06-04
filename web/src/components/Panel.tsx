import type { ReactNode } from "react";

interface PanelProps {
  title?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  bodyClassName?: string;
}

// Panel is the CRT-styled container used throughout the app: a glowing bordered
// box with an optional uppercase header and right-aligned actions.
export function Panel({ title, actions, children, className, bodyClassName }: PanelProps) {
  return (
    <section className={`panel flex min-h-0 flex-col ${className ?? ""}`}>
      {(title || actions) && (
        <header className="panel-header flex items-center justify-between gap-2">
          <span>{title}</span>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className={`min-h-0 flex-1 p-4 ${bodyClassName ?? ""}`}>{children}</div>
    </section>
  );
}
