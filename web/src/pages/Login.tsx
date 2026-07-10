import { AuthLayout } from '../components/ui/auth-layout';
import { Button } from '../components/ui/button';
import { Field, FieldGroup, Fieldset, Label, Legend } from '../components/ui/fieldset';
import { Input } from '../components/ui/input';
import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';

export default function Login() {
  return (
    <AuthLayout>
      <form action="#" method="post" className="grid w-full max-w-sm grid-cols-1 gap-8">
        <Heading>Sign in to 5gpn</Heading>
        <Text>Single-user personal gateway panel. M0 skeleton — auth wiring lands in M1.</Text>
        <Fieldset>
          <Legend className="sr-only">Credentials</Legend>
          <FieldGroup>
            <Field>
              <Label>Username</Label>
              <Input name="username" type="text" autoComplete="username" />
            </Field>
            <Field>
              <Label>Password</Label>
              <Input name="password" type="password" autoComplete="current-password" />
            </Field>
          </FieldGroup>
        </Fieldset>
        <Button type="submit" className="w-full">
          Sign in
        </Button>
      </form>
    </AuthLayout>
  );
}
