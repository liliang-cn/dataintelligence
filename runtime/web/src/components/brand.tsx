/** SmartFactory's hex block, so the two products read as one suite. */
export function Brand({ size = 30 }: { size?: number }) {
  return (
    <svg viewBox="0 0 32 32" width={size} height={size} aria-hidden className="shrink-0">
      <path d="M16 3 28 9.5v13L16 29 4 22.5v-13z" fill="#2f5bea" />
      <path d="M16 3 28 9.5 16 16 4 9.5z" fill="#6f8ff5" />
      <path d="M16 16v13L4 22.5v-13z" fill="#1f3fb0" />
    </svg>
  );
}
