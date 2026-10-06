import ReactMarkdown, { type Components } from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';

const components: Components = {
  h1: ({ children }) => <h3 className="mt-5 mb-2 text-[17px] font-extrabold first:mt-0">{children}</h3>,
  h2: ({ children }) => <h3 className="mt-5 mb-2 text-[16px] font-extrabold first:mt-0">{children}</h3>,
  h3: ({ children }) => <h4 className="mt-4 mb-1.5 text-[15px] font-bold first:mt-0">{children}</h4>,
  h4: ({ children }) => <h5 className="mt-3 mb-1 text-[14px] font-bold first:mt-0">{children}</h5>,
  p: ({ children }) => <p className="my-2.5 leading-[1.8] first:mt-0 last:mb-0">{children}</p>,
  ul: ({ children }) => <ul className="my-2.5 grid list-disc gap-1 pl-5 marker:text-primary/60">{children}</ul>,
  ol: ({ children }) => <ol className="my-2.5 grid list-decimal gap-1 pl-5 marker:font-bold marker:text-primary/70">{children}</ol>,
  li: ({ children }) => <li className="pl-1 leading-[1.75]">{children}</li>,
  strong: ({ children }) => <strong className="font-bold text-foreground">{children}</strong>,
  a: ({ children, href }) => (
    <a href={href} className="font-semibold text-primary underline-offset-4 hover:underline" target={href?.startsWith('/') ? undefined : '_blank'} rel="noreferrer">
      {children}
    </a>
  ),
  blockquote: ({ children }) => <blockquote className="my-3 rounded-r-lg border-l-[3px] border-primary/50 bg-accent/50 px-4 py-2 text-[14px]">{children}</blockquote>,
  hr: () => <hr className="my-4 border-border" />,
  code: ({ children, className }) =>
    className ? (
      <code className="block overflow-x-auto rounded-xl bg-muted p-3 font-mono text-[12px]">{children}</code>
    ) : (
      <code className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-[0.88em]">{children}</code>
    ),
  pre: ({ children }) => <pre className="my-3">{children}</pre>,
  table: ({ children }) => (
    <div className="my-3 overflow-hidden rounded-xl border">
      <Table className="text-[13px]">{children}</Table>
    </div>
  ),
  thead: ({ children }) => <TableHeader className="bg-surface">{children}</TableHeader>,
  tbody: ({ children }) => <TableBody>{children}</TableBody>,
  tr: ({ children }) => <TableRow>{children}</TableRow>,
  th: ({ children, style }) => <TableHead className="h-9 px-3 font-semibold whitespace-nowrap text-muted-foreground" style={style}>{children}</TableHead>,
  td: ({ children, style }) => <TableCell className="px-3 py-2 tabular-nums" style={style}>{children}</TableCell>,
};

export function Markdown({ text }: { text: string }) {
  return (
    <div className="text-[14.5px] text-foreground/95">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
}
