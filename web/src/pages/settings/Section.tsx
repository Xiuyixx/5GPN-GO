import type { ReactNode } from 'react';
import { Subheading } from '../../components/ui/heading';
import { Text } from '../../components/ui/text';

export function Section({
  tint,
  title,
  description,
  children,
}: {
  tint: string;
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <div className="glass tile relative overflow-hidden p-6">
      <div
        aria-hidden
        className="absolute inset-x-0 -top-8 h-24 opacity-70 blur-2xl"
        style={{ background: `radial-gradient(60% 100% at 30% 0%, ${tint}, transparent 70%)` }}
      />
      <div className="relative">
        <Subheading>{title}</Subheading>
        {description && <Text className="mt-2">{description}</Text>}
        <div className="mt-4">{children}</div>
      </div>
    </div>
  );
}
