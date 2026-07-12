import { useTranslation } from 'react-i18next';
import AppShell from '../layouts/AppShell';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { InternalOnlySection } from './settings/InternalOnlySection';
import { IOSSection } from './settings/IOSSection';
import { MTProxySection } from './settings/MTProxySection';
import { PasswordSection } from './settings/PasswordSection';
import { RestartSection } from './settings/RestartSection';
import { TgbotSection } from './settings/TgbotSection';
import { TransparentProxySection } from './settings/TransparentProxySection';
import { UpgradeSection } from './settings/UpgradeSection';

export default function Settings() {
  const { t } = useTranslation();
  return (
    <AppShell>
      <div className="mb-6">
        <Heading>{t('nav.settings')}</Heading>
        <Text className="mt-1">{t('settings.subtitle')}</Text>
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <UpgradeSection />
        <TgbotSection />
        <IOSSection />
        <MTProxySection />
        <InternalOnlySection />
        <TransparentProxySection />
        <PasswordSection />
        <RestartSection />
      </div>
    </AppShell>
  );
}
