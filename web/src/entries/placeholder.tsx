export function EntryPlaceholder({ title, description }: { title: string; description: string }) {
  return (
    <main className="grid min-h-screen place-items-center bg-paper px-6 text-ink">
      <div className="rounded-panel border border-rule bg-panel px-8 py-6">
        <h1 className="text-page font-semibold tracking-tight">{title}</h1>
        <p className="mt-2 text-sm text-ink-2">{description}</p>
      </div>
    </main>
  );
}
